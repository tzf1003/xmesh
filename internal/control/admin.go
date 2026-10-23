package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// IngressAdmin is the running ingress as the control channel sees it.
type IngressAdmin interface {
	Issuer() string
	Clients() []v2.IngressClient
	// RevokeClient removes a client and its tokens; false means there was no such client.
	RevokeClient(id string) (bool, error)
	// RotatePassword stores the new login password and makes it effective.
	RotatePassword(password string) error
}

// pathPart returns the i-th segment of the request path ("/v2/pats/x/rotate": 0 is "v2").
func pathPart(r *http.Request, i int) string {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// --- pairing (CTR-CTL-08) ---

// deviceState uses the running shared trust service. Local-only management can also prepare
// its version 2 identity and trust before any network provider is enabled.
func (s *Server) deviceState(ctx context.Context) (*device.Identity, *device.TrustStore, error) {
	if s.o.DeviceIdentity != nil && s.o.DeviceTrust != nil {
		return s.o.DeviceIdentity, s.o.DeviceTrust, nil
	}
	id, err := device.LoadIdentity(ctx, s.o.StateDir)
	if err != nil {
		return nil, nil, err
	}
	trust, err := device.OpenTrust(s.o.StateDir)
	return id, trust, err
}

func (s *Server) pairExport(w http.ResponseWriter, r *http.Request, _ *call) {
	id, _, err := s.deviceState(r.Context())
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "无法读取设备身份"))
		return
	}
	doc := id.Document(s.o.Store.Get().Node.Name)
	writeJSON(w, 200, v2.PairExportResponse{Pair: v2.PairDocument{Version: doc.Version, DeviceID: doc.DeviceID, PublicKey: doc.PublicKey, Name: doc.Name, Fingerprint: strings.TrimPrefix(doc.DeviceID, "dev-"), Tags: []string{}}})
}

var pairTags = map[string]bool{"tag:peer": true, "tag:device": true, "tag:ingress": true, "tag:admin": true}

func normalizeFingerprint(text string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), ":", ""))
}

func (s *Server) deleteDeviceTrust(w http.ResponseWriter, r *http.Request, _ *call) {
	id := pathPart(r, 3)
	if !strings.HasPrefix(id, "dev-") || !pairFingerprintPattern.MatchString(strings.TrimPrefix(id, "dev-")) {
		failWith(w, argError("device_id", "设备 ID 无效"))
		return
	}
	_, trust, err := s.deviceState(r.Context())
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "设备信任服务不可用"))
		return
	}
	if _, ok := trust.Find(id); !ok {
		failWith(w, newError(v2.ErrNotFound, "该设备尚未配对"))
		return
	}
	if err := trust.Revoke(r.Context(), id); err != nil {
		failWith(w, newError(v2.ErrInternal, "无法撤销设备信任"))
		return
	}
	w.WriteHeader(204)
}

func (s *Server) updateDeviceTrust(w http.ResponseWriter, r *http.Request, _ *call) {
	id := pathPart(r, 3)
	if !strings.HasPrefix(id, "dev-") || !pairFingerprintPattern.MatchString(strings.TrimPrefix(id, "dev-")) {
		failWith(w, argError("device_id", "设备 ID 无效"))
		return
	}
	var req v2.PairTrustUpdateRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	tags := []string{}
	for _, tag := range req.Tags {
		if !pairTags[tag] {
			failWith(w, argError("tags", "设备角色无效"))
			return
		}
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		failWith(w, argError("tags", "至少保留一个设备角色；要移除设备请撤销设备信任"))
		return
	}
	_, trust, err := s.deviceState(r.Context())
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "设备信任服务不可用"))
		return
	}
	updated, err := trust.SetTags(r.Context(), id, tags)
	if errors.Is(err, device.ErrUntrusted) {
		failWith(w, newError(v2.ErrNotFound, "该设备尚未配对"))
		return
	}
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "无法保存设备角色"))
		return
	}
	writeJSON(w, 200, v2.PairTrustUpdateResponse{DeviceID: id, Tags: updated.Tags})
}

func (s *Server) pairImport(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v2.PairImportRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	doc := device.Document{Version: req.Pair.Version, DeviceID: req.Pair.DeviceID, Name: req.Pair.Name, PublicKey: req.Pair.PublicKey}
	confirmed := normalizeFingerprint(req.Fingerprint)
	if confirmed == "" || confirmed != normalizeFingerprint(req.Pair.Fingerprint) || "dev-"+confirmed != doc.DeviceID {
		failWith(w, newError(v2.ErrFingerprintMismatch, "设备公钥指纹与独立确认的指纹不一致"))
		return
	}
	if err := doc.Validate(); err != nil {
		failWith(w, argError("pair", "设备配对文档无效，请使用新版导出文档"))
		return
	}
	tags := []string{}
	for _, tag := range req.Tags {
		if !pairTags[tag] {
			failWith(w, argError("tags", "设备角色无效"))
			return
		}
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		tags = []string{"tag:peer", "tag:device"}
	}
	id, trust, err := s.deviceState(r.Context())
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "设备信任服务不可用"))
		return
	}
	if id.ID == doc.DeviceID {
		failWith(w, argError("pair", "无法与本机自身配对"))
		return
	}
	if err := trust.Grant(r.Context(), doc, tags, nil); err != nil {
		failWith(w, newError(v2.ErrInternal, "无法保存设备信任"))
		return
	}
	writeJSON(w, 200, v2.PairImportResponse{Name: doc.Name, Fingerprint: strings.TrimPrefix(doc.DeviceID, "dev-"), Tags: tags, RestartRequired: false})
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
		// Windows reports access denied when exclusive creation meets a directory.
		if _, statErr := os.Lstat(path); statErr == nil {
			return &os.PathError{Op: "create", Path: path, Err: os.ErrExist}
		}
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

func patInfo(name string, pat config.PAT) v2.PatInfo {
	info := v2.PatInfo{Name: name, Scopes: append([]string{}, pat.Scopes...), TokenFile: pat.TokenFile}
	if pat.TokenFile != "" {
		if absolute, err := filepath.Abs(pat.TokenFile); err == nil {
			info.TokenFile = absolute
		}
	}
	if st, err := os.Stat(pat.TokenFile); err == nil {
		t := v2.At(st.ModTime())
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
	out := v2.PatList{Pats: []v2.PatInfo{}, Enforced: len(pats) > 0}
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

// editConfigLocked applies edits to the file, validates the result like PUT /v2/config and stores
// it. The caller holds configMu. A failure is reported to w and returns false.
func (s *Server) editConfigLocked(w http.ResponseWriter, old []byte, edits []Edit) bool {
	next, err := ApplyEdits(old, edits)
	if err != nil {
		failWith(w, newError(v2.ErrConflict, "配置结构不允许这项修改"))
		return false
	}
	if _, err = config.Parse(next); err != nil {
		e := newError(v2.ErrConfigInvalid, "配置未通过校验，没有写入")
		e.Diagnostics = diagnostics(err)
		failWith(w, e)
		return false
	}
	if _, _, err = s.storeConfigLocked(old, next, diffConfigs(parsedOf(old), parsedOf(next))); err != nil {
		if errors.Is(err, errConfigChanged) {
			failWith(w, newError(v2.ErrConflict, "配置已变化，请重新读取后重试"))
			return false
		}
		failWith(w, newError(v2.ErrInternal, "写入配置文件失败"))
		return false
	}
	return true
}

func (s *Server) createPat(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v2.PatCreateRequest
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
		failWith(w, newError(v2.ErrInternal, "无法读取配置文件"))
		return
	}
	if _, exists := patEntries(old)[req.Name]; exists {
		failWith(w, newError(v2.ErrConflict, "同名 PAT 已存在"))
		return
	}
	if gateway, _ := parsedOf(old)["gateway"].(map[string]any); gateway["listen"] == nil {
		// Without a gateway section the new entry alone would not be a valid configuration.
		e := newError(v2.ErrConflict, "此节点没有配置 gateway，无法创建 PAT")
		e.Reason = "gateway_not_configured"
		failWith(w, e)
		return
	}
	token, err := newPatToken()
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "无法生成令牌"))
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
		failWith(w, newError(v2.ErrInternal, "无法写入令牌文件"))
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
	writeJSON(w, 201, v2.PatSecret{Pat: patInfo(req.Name, config.PAT{TokenFile: file, Scopes: scopes}), Token: token})
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
		failWith(w, newError(v2.ErrInternal, "无法读取配置文件"))
		return
	}
	pat, ok := patEntries(old)[name]
	if !ok || pat.TokenFile == "" {
		failWith(w, newError(v2.ErrNotFound, "没有这个 PAT"))
		return
	}
	token, err := newPatToken()
	if err == nil {
		err = writeAtomic(pat.TokenFile, []byte(token+"\n"))
	}
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "无法写入令牌文件"))
		return
	}
	writeJSON(w, 200, v2.PatSecret{Pat: patInfo(name, pat), Token: token})
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
		failWith(w, newError(v2.ErrInternal, "无法读取配置文件"))
		return
	}
	pat, ok := patEntries(old)[name]
	if !ok {
		failWith(w, newError(v2.ErrNotFound, "没有这个 PAT"))
		return
	}
	if len(patEntries(old)) == 1 {
		// The gateway always requires a token (CTR-GW-07), so the last one stays until it is rotated.
		e := newError(v2.ErrConflict, "至少保留一个令牌：本机 Gateway 每个请求都需要令牌")
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
				failWith(w, newError(v2.ErrInternal, "PAT 已撤销，但令牌文件清理失败"))
				return
			}
			if err := os.Remove(pat.TokenFile + ".owner"); err != nil && !os.IsNotExist(err) {
				failWith(w, newError(v2.ErrInternal, "PAT 已撤销，但归属凭据清理失败"))
				return
			}
		}
	}
	w.WriteHeader(204)
}

// --- ingress (CTR-CTL-10) ---

func (s *Server) ingressAdmin(w http.ResponseWriter) IngressAdmin {
	if !s.o.Store.Get().HasRole("ingress") {
		failWith(w, newError(v2.ErrRoleDisabled, "此节点没有 ingress 角色"))
		return nil
	}
	admin := s.o.Ingress
	if s.o.IngressNow != nil {
		admin = s.o.IngressNow()
	}
	if admin == nil {
		failWith(w, newError(v2.ErrUnavailable, "入口尚未就绪"))
		return nil
	}
	return admin
}

func (s *Server) listIngressClients(w http.ResponseWriter, _ *http.Request, _ *call) {
	admin := s.ingressAdmin(w)
	if admin == nil {
		return
	}
	out := v2.IngressClientsResponse{Clients: admin.Clients()}
	if out.Clients == nil {
		out.Clients = []v2.IngressClient{}
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
		failWith(w, newError(v2.ErrInternal, "无法保存客户端列表"))
	case !found:
		failWith(w, newError(v2.ErrNotFound, "没有这个客户端"))
	default:
		w.WriteHeader(204)
	}
}

func (s *Server) putIngressPassword(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ingressAdmin(w)
	if admin == nil {
		return
	}
	var req v2.IngressPasswordRequest
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
			e := newError(v2.ErrConflict, "登录口令不是来自文件，无法通过控制通道轮转")
			e.Reason = "password_source_not_file"
			failWith(w, e)
			return
		}
		failWith(w, newError(v2.ErrInternal, "无法保存登录口令"))
		return
	}
	writeJSON(w, 200, v2.IngressPasswordResponse{RotatedAt: v2.Now()})
}

// PasswordSourceError is returned by IngressAdmin.RotatePassword when the password did not come
// from a file the kernel can rewrite.
type PasswordSourceError struct{}

func (*PasswordSourceError) Error() string { return "password is not read from a file" }
