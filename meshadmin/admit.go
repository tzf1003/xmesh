package meshadmin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"tailscale.com/tailcfg"
)

var nodeKeyPattern = regexp.MustCompile(`^nodekey:[0-9a-f]{64}$`)

// NormalizeNodeKey returns the canonical "nodekey:<64 hex>" form of a node
// public key as Tailscale and Headscale print it, or an error.
func NormalizeNodeKey(s string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(k, "nodekey:") {
		k = "nodekey:" + k
	}
	if !nodeKeyPattern.MatchString(k) {
		return "", fmt.Errorf("%w: %q is not a node public key", ErrInvalid, s)
	}
	return k, nil
}

// AllowEntry is one node public key admitted to the DERP server.
type AllowEntry struct {
	Key   string    `json:"key"`
	Name  string    `json:"name,omitempty"`
	Added time.Time `json:"added"`
}

type allowFile struct {
	Version int          `json:"version"`
	Keys    []AllowEntry `json:"keys"`
}

// Allowlist is the file-backed set of admitted node keys. Reads re-check the
// file's modification time, so `xmesh mesh derp sync` takes effect without
// restarting the admission server.
type Allowlist struct {
	path string
	mu   sync.Mutex
	mod  time.Time
	size int64
	set  map[string]AllowEntry
}

// OpenAllowlist opens (or lazily creates) the allowlist at path.
func OpenAllowlist(path string) (*Allowlist, error) {
	a := &Allowlist{path: path, set: map[string]AllowEntry{}}
	if err := a.reload(true); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Allowlist) reload(force bool) error {
	st, err := os.Stat(a.path)
	if os.IsNotExist(err) {
		a.set, a.mod, a.size = map[string]AllowEntry{}, time.Time{}, 0
		return nil
	}
	if err != nil {
		return err
	}
	if !force && st.ModTime().Equal(a.mod) && st.Size() == a.size {
		return nil
	}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return err
	}
	var f allowFile
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("meshadmin: allowlist %s is corrupt: %w", a.path, err)
	}
	set := make(map[string]AllowEntry, len(f.Keys))
	for _, e := range f.Keys {
		k, err := NormalizeNodeKey(e.Key)
		if err != nil {
			return fmt.Errorf("meshadmin: allowlist %s: %w", a.path, err)
		}
		e.Key = k
		set[k] = e
	}
	a.set, a.mod, a.size = set, st.ModTime(), st.Size()
	return nil
}

// Allowed reports whether key is admitted. A corrupt or unreadable file denies
// everything: the list is the allow side, so failing closed is the safe default.
func (a *Allowlist) Allowed(key string) bool {
	k, err := NormalizeNodeKey(key)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reload(false); err != nil {
		return false
	}
	_, ok := a.set[k]
	return ok
}

// Entries returns the current entries sorted by key.
func (a *Allowlist) Entries() ([]AllowEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reload(false); err != nil {
		return nil, err
	}
	out := make([]AllowEntry, 0, len(a.set))
	for _, e := range a.set {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Replace atomically installs entries as the whole allowlist. Entries already
// present keep their original Added time.
func (a *Allowlist) Replace(entries []AllowEntry) (added, removed int, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reload(false); err != nil {
		return 0, 0, err
	}
	next := make(map[string]AllowEntry, len(entries))
	now := time.Now().UTC()
	for _, e := range entries {
		k, err := NormalizeNodeKey(e.Key)
		if err != nil {
			return 0, 0, err
		}
		if old, ok := a.set[k]; ok {
			e.Added = old.Added
		} else {
			added++
			e.Added = now
		}
		e.Key = k
		next[k] = e
	}
	for k := range a.set {
		if _, ok := next[k]; !ok {
			removed++
		}
	}
	list := make([]AllowEntry, 0, len(next))
	for _, e := range next {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	b, err := json.MarshalIndent(allowFile{Version: 1, Keys: list}, "", "  ")
	if err != nil {
		return 0, 0, err
	}
	if err := writeFileAtomic(a.path, b); err != nil {
		return 0, 0, err
	}
	a.set = next
	if st, err := os.Stat(a.path); err == nil {
		a.mod, a.size = st.ModTime(), st.Size()
	}
	return added, removed, nil
}

// writeFileAtomic writes b to path with mode 0600 via a temp file and rename.
func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if !isWindows() {
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteSecretFile writes secret to path with mode 0600, refusing to overwrite
// unless overwrite is set. It is used for auth key files.
func WriteSecretFile(path string, secret []byte, overwrite bool) error {
	if !overwrite {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%w: %s already exists", ErrInvalid, path)
		}
	}
	return writeFileAtomic(path, secret)
}

// AdmissionServer implements the derper --verify-client-url contract
// (tailcfg.DERPAdmitClientRequest/Response) over an Allowlist, plus a small
// token-protected admin API used by `xmesh mesh derp sync --admission-url`.
//
// It authenticates nobody on /admit: derper calls it from the same host or
// private network, so listen on loopback or a private address. Admission is
// checked when a client connects; connections that are already up stay up until
// they reconnect.
type AdmissionServer struct {
	List       *Allowlist
	AdminToken string // empty disables the admin API
	Logf       func(format string, args ...any)
}

const maxAdmitBody = 4 << 10

func (s *AdmissionServer) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// Handler returns the HTTP handler: POST /admit, GET|PUT /admin/keys, GET /healthz.
func (s *AdmissionServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admit", s.admit)
	mux.HandleFunc("/admin/keys", s.admin)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	return mux
}

func (s *AdmissionServer) admit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAdmitBody+1))
	if err != nil || len(body) > maxAdmitBody {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req tailcfg.DERPAdmitClientRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	text, _ := req.NodePublic.MarshalText()
	allow := s.List.Allowed(string(text))
	s.logf("derp admit key=%s source=%s allow=%t", shortKey(string(text)), req.Source, allow)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tailcfg.DERPAdmitClientResponse{Allow: allow})
}

func shortKey(k string) string {
	if len(k) > 16 {
		return k[:16] + "..."
	}
	return k
}

func (s *AdmissionServer) authorized(r *http.Request) bool {
	if s.AdminToken == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.AdminToken)) == 1
}

type adminKeys struct {
	Keys []AllowEntry `json:"keys"`
}

func (s *AdmissionServer) admin(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		entries, err := s.List.Entries()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminKeys{Keys: entries})
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20+1))
		if err != nil || len(body) > 4<<20 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var in adminKeys
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		added, removed, err := s.List.Replace(in.Keys)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logf("derp allowlist replaced: +%d -%d", added, removed)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"added": added, "removed": removed, "total": len(in.Keys)})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// SyncOptions select which devices become admitted.
type SyncOptions struct {
	// AnyTagged admits every authorised device that has at least one xmesh role tag
	// (default); AllDevices admits every authorised device.
	AllDevices bool
}

// SyncResult reports a sync.
type SyncResult struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Total   int `json:"total"`
}

// AdmissionEntries picks the devices to admit and returns their node keys.
func AdmissionEntries(devices []Device, opt SyncOptions) []AllowEntry {
	var out []AllowEntry
	for _, d := range devices {
		if !d.Authorized || d.NodeKey == "" {
			continue
		}
		if !opt.AllDevices && len(RolesFromTags(d.Tags)) == 0 {
			continue
		}
		k, err := NormalizeNodeKey(d.NodeKey)
		if err != nil {
			continue
		}
		out = append(out, AllowEntry{Key: k, Name: deviceLabel(d)})
	}
	return out
}

// SyncAllowlist replaces the local allowlist with the control plane's devices.
func SyncAllowlist(ctx context.Context, p Provider, a *Allowlist, opt SyncOptions) (SyncResult, error) {
	devs, err := p.Devices(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	entries := AdmissionEntries(devs, opt)
	added, removed, err := a.Replace(entries)
	return SyncResult{Added: added, Removed: removed, Total: len(entries)}, err
}

// SyncRemote pushes the control plane's devices to a remote AdmissionServer's
// admin API. baseURL is the admission server origin, token its admin token.
func SyncRemote(ctx context.Context, p Provider, baseURL, token string, hc *http.Client, opt SyncOptions) (SyncResult, error) {
	r, err := newRest(baseURL, hc)
	if err != nil {
		return SyncResult{}, err
	}
	devs, err := p.Devices(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	entries := AdmissionEntries(devs, opt)
	b, _ := json.Marshal(adminKeys{Keys: entries})
	rp, err := r.do(ctx, call{
		method: http.MethodPut, path: "/admin/keys", body: b, op: "update admission list",
		header: map[string]string{"Authorization": "Bearer " + token},
	})
	if err != nil {
		return SyncResult{}, err
	}
	var res SyncResult
	if err := json.NewDecoder(bytes.NewReader(rp.body)).Decode(&res); err != nil {
		return SyncResult{}, fmt.Errorf("meshadmin: update admission list: decode: %w", err)
	}
	return res, nil
}

// NewAdminToken returns a random 256-bit hex token for the admission admin API.
func NewAdminToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
