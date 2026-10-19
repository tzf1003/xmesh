package control

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// IngressAdmin is the running ingress as the control channel sees it.
type IngressAdmin interface {
	Issuer() string
	Clients() []v1.IngressClient
	// RevokeClient removes a client and its tokens; false means there was no such client.
	RevokeClient(id string) (bool, error)
	// RotatePassword stores the new login password and makes it effective.
	RotatePassword(password string) error
}

// pathPart returns the i-th segment of the request path ("/v1/pats/x/rotate": 0 is "v1").
func pathPart(r *http.Request, i int) string {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// --- pairing (CTR-CTL-08) ---

// lanPairDir is where the LAN certificate and trust store live; the CLI uses the same directory.
func (s *Server) lanPairDir() string { return filepath.Join(s.o.StateDir, "lan") }

func (s *Server) requireLAN(w http.ResponseWriter) bool {
	if s.o.Store.Get().Mesh.Mode != "lan" {
		failWith(w, newError(v1.ErrUnsupportedMode, "配对仅用于 lan 模式"))
		return false
	}
	return true
}

func (s *Server) pairExport(w http.ResponseWriter, _ *http.Request, _ *call) {
	if !s.requireLAN(w) {
		return
	}
	cert, err := mesh.LoadCertificate(s.lanPairDir(), s.o.Store.Get().Node.Name)
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取本机 LAN 证书"))
		return
	}
	writeJSON(w, 200, v1.PairExportResponse{Pair: v1.PairDocument{Name: s.o.Store.Get().Node.Name, Fingerprint: cert.Fingerprint, CertificatePEM: string(cert.PEM), Tags: []string{}}})
}

var pairTags = map[string]bool{"tag:peer": true, "tag:device": true, "tag:ingress": true}

func normalizeFingerprint(text string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), ":", ""))
}

func (s *Server) pairImport(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v1.PairImportRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if !s.requireLAN(w) {
		return
	}
	tags := []string{}
	seen := map[string]bool{}
	for _, tag := range req.Tags {
		if !pairTags[tag] {
			failWith(w, argError("tags", "tags 只能是 tag:peer、tag:device、tag:ingress"))
			return
		}
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		tags = []string{"tag:peer", "tag:device"}
	}
	if req.Pair.Name == "" || req.Pair.CertificatePEM == "" {
		failWith(w, argError("pair", "缺少对端配对内容"))
		return
	}
	// The fingerprint the user confirmed must match both the document and the certificate in it.
	confirmed := normalizeFingerprint(req.Fingerprint)
	computed, err := mesh.CertificateFingerprint(req.Pair.CertificatePEM)
	if err != nil {
		failWith(w, argError("pair.certificate_pem", "证书无效"))
		return
	}
	if confirmed == "" || confirmed != normalizeFingerprint(req.Pair.Fingerprint) || confirmed != computed {
		failWith(w, newError(v1.ErrFingerprintMismatch, "指纹与配对文件或证书不一致"))
		return
	}
	if err = privdir.Ensure(s.o.StateDir); err != nil {
		failWith(w, newError(v1.ErrInternal, "状态目录不可用"))
		return
	}
	peer := mesh.Trusted{Name: req.Pair.Name, Fingerprint: computed, CertificatePEM: req.Pair.CertificatePEM}
	if err = mesh.ImportTrusted(s.lanPairDir(), peer, confirmed, tags); err != nil {
		if errors.Is(err, mesh.ErrPairFingerprint) {
			failWith(w, newError(v1.ErrFingerprintMismatch, "指纹与配对文件或证书不一致"))
		} else {
			failWith(w, argError("pair", "对端配对内容无效"))
		}
		return
	}
	s.pairPending.Store(true)
	writeJSON(w, 200, v1.PairImportResponse{Name: peer.Name, Fingerprint: computed, Tags: tags, RestartRequired: true})
}

// --- personal access tokens (CTR-CTL-09) ---

var patNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
var patFileSuffixPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (s *Server) patDir() string { return patDirOf(s.o.StateDir) }

func patDirOf(stateDir string) string { return filepath.Join(stateDir, "pats") }

func managedPatPath(dir, name, file string) bool {
	if filepath.Dir(filepath.Clean(file)) != filepath.Clean(dir) {
		return false
	}
	base := filepath.Base(file)
	if base == name+".token" {
		return true
	}
	suffix, ok := strings.CutPrefix(base, name+"-")
	if !ok {
		return false
	}
	suffix, ok = strings.CutSuffix(suffix, ".token")
	return ok && patFileSuffixPattern.MatchString(suffix)
}

// Interrupted creation/deletion never blocks a fresh credential or overwrites retained files.
func writeAvailablePatFile(dir, name, token string) (string, error) {
	file := filepath.Join(dir, name+".token")
	err := writeManagedPatFile(file, name, token)
	if errors.Is(err, os.ErrExist) {
		suffix, suffixErr := newPatToken()
		if suffixErr != nil {
			return "", suffixErr
		}
		file = filepath.Join(dir, name+"-"+suffix+".token")
		err = writeManagedPatFile(file, name, token)
	}
	return file, err
}

// Create the token and its receipt together for both explicit and automatic PATs.
func writeManagedPatFile(file, name, token string) error {
	if err := writeNewPatFile(file, []byte(token+"\n")); err != nil {
		return err
	}
	if err := writeNewPatFile(file+".owner", []byte(name+"\n")); err != nil {
		_ = os.Remove(file)
		return err
	}
	return nil
}

// A pre-existing credential or receipt belongs to its current owner.
func writeNewPatFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func newPatToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func patInfo(name string, pat config.PAT) v1.PatInfo {
	info := v1.PatInfo{Name: name, Scopes: append([]string{}, pat.Scopes...), TokenFile: pat.TokenFile}
	if st, err := os.Stat(pat.TokenFile); err == nil {
		t := v1.At(st.ModTime())
		info.ModifiedAt = &t
	}
	return info
}

func (s *Server) listPats(w http.ResponseWriter, _ *http.Request, _ *call) {
	pats := s.o.Store.Get().Gateway.PATs
	names := make([]string, 0, len(pats))
	for name := range pats {
		names = append(names, name)
	}
	sort.Strings(names)
	out := v1.PatList{Pats: []v1.PatInfo{}, Enforced: len(pats) > 0}
	for _, name := range names {
		out.Pats = append(out.Pats, patInfo(name, pats[name]))
	}
	writeJSON(w, 200, out)
}

// patEntries are the gateway.pats of the file as it is now, by name.
func patEntries(raw []byte) map[string]config.PAT {
	out := map[string]config.PAT{}
	for name, entry := range patsOf(parsedOf(raw)) {
		fields, _ := entry.(map[string]any)
		pat := config.PAT{}
		pat.TokenFile, _ = fields["token_file"].(string)
		if scopes, ok := fields["scopes"].([]any); ok {
			for _, scope := range scopes {
				if text, ok := scope.(string); ok {
					pat.Scopes = append(pat.Scopes, text)
				}
			}
		}
		out[name] = pat
	}
	return out
}

// editConfigLocked applies edits to the file, validates the result like PUT /v1/config and stores
// it. The caller holds configMu. A failure is reported to w and returns false.
func (s *Server) editConfigLocked(w http.ResponseWriter, old []byte, edits []Edit) bool {
	next, err := ApplyEdits(old, edits)
	if err != nil {
		failWith(w, newError(v1.ErrConflict, "配置结构不允许这项修改"))
		return false
	}
	if _, err = config.Parse(next); err != nil {
		e := newError(v1.ErrConfigInvalid, "配置未通过校验，没有写入")
		e.Diagnostics = diagnostics(err)
		failWith(w, e)
		return false
	}
	if _, _, err = s.storeConfigLocked(next, diffConfigs(parsedOf(old), parsedOf(next))); err != nil {
		failWith(w, newError(v1.ErrInternal, "写入配置文件失败"))
		return false
	}
	return true
}

func (s *Server) createPat(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v1.PatCreateRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if !patNamePattern.MatchString(req.Name) {
		failWith(w, argError("name", "name 只能含字母、数字和连字符，且以字母或数字开头"))
		return
	}
	if len(req.Scopes) == 0 {
		failWith(w, argError("scopes", "至少需要一个 scope"))
		return
	}
	if _, err := oauth.Expand(req.Scopes); err != nil {
		failWith(w, argError("scopes", "scope 无效"))
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	if _, exists := patEntries(old)[req.Name]; exists {
		failWith(w, newError(v1.ErrConflict, "同名 PAT 已存在"))
		return
	}
	if gateway, _ := parsedOf(old)["gateway"].(map[string]any); gateway["listen"] == nil {
		// Without a gateway section the new entry alone would not be a valid configuration.
		e := newError(v1.ErrConflict, "此节点没有配置 gateway，无法创建 PAT")
		e.Reason = "gateway_not_configured"
		failWith(w, e)
		return
	}
	token, err := newPatToken()
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法生成令牌"))
		return
	}
	if err = privdir.Ensure(s.o.StateDir); err == nil {
		err = privdir.Ensure(s.patDir())
	}
	var file string
	if err == nil {
		file, err = writeAvailablePatFile(s.patDir(), req.Name, token)
	}
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法写入令牌文件"))
		return
	}
	// The token file exists before the entry that names it: the Gateway refuses every PAT while
	// any listed file is unreadable.
	scopes := append([]string{}, req.Scopes...)
	if !s.editConfigLocked(w, old, []Edit{{Path: "/gateway/pats/" + req.Name, Value: map[string]any{"token_file": file, "scopes": scopes}}}) {
		_ = os.Remove(file)
		_ = os.Remove(file + ".owner")
		return
	}
	writeJSON(w, 201, v1.PatSecret{Pat: patInfo(req.Name, config.PAT{TokenFile: file, Scopes: scopes}), Token: token})
}

func (s *Server) rotatePat(w http.ResponseWriter, r *http.Request, _ *call) {
	name := pathPart(r, 2)
	if !patNamePattern.MatchString(name) {
		failWith(w, argError("name", "PAT 名称无效"))
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	pat, ok := patEntries(old)[name]
	if !ok || pat.TokenFile == "" {
		failWith(w, newError(v1.ErrNotFound, "没有这个 PAT"))
		return
	}
	token, err := newPatToken()
	if err == nil {
		err = writeAtomic(pat.TokenFile, []byte(token+"\n"))
	}
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法写入令牌文件"))
		return
	}
	writeJSON(w, 200, v1.PatSecret{Pat: patInfo(name, pat), Token: token})
}

func (s *Server) deletePat(w http.ResponseWriter, r *http.Request, _ *call) {
	name := pathPart(r, 2)
	if !patNamePattern.MatchString(name) {
		failWith(w, argError("name", "PAT 名称无效"))
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	pat, ok := patEntries(old)[name]
	if !ok {
		failWith(w, newError(v1.ErrNotFound, "没有这个 PAT"))
		return
	}
	if len(patEntries(old)) == 1 {
		// The gateway always requires a token (CTR-GW-07), so the last one stays until it is rotated.
		e := newError(v1.ErrConflict, "至少保留一个令牌：本机 Gateway 每个请求都需要令牌")
		e.Reason = "last_pat"
		failWith(w, e)
		return
	}
	if !s.editConfigLocked(w, old, []Edit{{Path: "/gateway/pats/" + name, Delete: true}}) {
		return
	}
	// Only a file the channel created is removed; a token file the operator pointed at stays.
	if managedPatPath(s.patDir(), name, pat.TokenFile) {
		if owner, err := os.ReadFile(pat.TokenFile + ".owner"); err == nil && string(owner) == name+"\n" {
			if err := os.Remove(pat.TokenFile); err != nil && !os.IsNotExist(err) {
				failWith(w, newError(v1.ErrInternal, "PAT 已撤销，但令牌文件清理失败"))
				return
			}
			if err := os.Remove(pat.TokenFile + ".owner"); err != nil && !os.IsNotExist(err) {
				failWith(w, newError(v1.ErrInternal, "PAT 已撤销，但归属凭据清理失败"))
				return
			}
		}
	}
	w.WriteHeader(204)
}

// --- ingress (CTR-CTL-10) ---

func (s *Server) ingressAdmin(w http.ResponseWriter) IngressAdmin {
	if !s.o.Store.Get().HasRole("ingress") {
		failWith(w, newError(v1.ErrRoleDisabled, "此节点没有 ingress 角色"))
		return nil
	}
	admin := s.o.Ingress
	if s.o.IngressNow != nil {
		admin = s.o.IngressNow()
	}
	if admin == nil {
		failWith(w, newError(v1.ErrUnavailable, "入口尚未就绪"))
		return nil
	}
	return admin
}

func (s *Server) listIngressClients(w http.ResponseWriter, _ *http.Request, _ *call) {
	admin := s.ingressAdmin(w)
	if admin == nil {
		return
	}
	out := v1.IngressClientsResponse{Clients: admin.Clients()}
	if out.Clients == nil {
		out.Clients = []v1.IngressClient{}
	}
	if issuer := admin.Issuer(); issuer != "" {
		out.Issuer = &issuer
	}
	writeJSON(w, 200, out)
}

func (s *Server) deleteIngressClient(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ingressAdmin(w)
	if admin == nil {
		return
	}
	found, err := admin.RevokeClient(pathPart(r, 3))
	switch {
	case err != nil:
		failWith(w, newError(v1.ErrInternal, "无法保存客户端列表"))
	case !found:
		failWith(w, newError(v1.ErrNotFound, "没有这个客户端"))
	default:
		w.WriteHeader(204)
	}
}

func (s *Server) putIngressPassword(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ingressAdmin(w)
	if admin == nil {
		return
	}
	var req v1.IngressPasswordRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if n := utf8.RuneCountInString(req.Password); n < 12 || n > 256 {
		failWith(w, argError("password", "口令长度须为 12 到 256 个字符"))
		return
	}
	if strings.TrimSpace(req.Password) != req.Password {
		// The kernel trims the password file when it reads it, so edges would not survive a restart.
		failWith(w, argError("password", "口令首尾不能有空白"))
		return
	}
	if err := admin.RotatePassword(req.Password); err != nil {
		var source *PasswordSourceError
		if errors.As(err, &source) {
			e := newError(v1.ErrConflict, "登录口令不是来自文件，无法通过控制通道轮转")
			e.Reason = "password_source_not_file"
			failWith(w, e)
			return
		}
		failWith(w, newError(v1.ErrInternal, "无法保存登录口令"))
		return
	}
	writeJSON(w, 200, v1.IngressPasswordResponse{RotatedAt: v1.Now()})
}

// PasswordSourceError is returned by IngressAdmin.RotatePassword when the password did not come
// from a file the kernel can rewrite.
type PasswordSourceError struct{}

func (*PasswordSourceError) Error() string { return "password is not read from a file" }
