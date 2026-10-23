package device

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
)

var (
	ErrUntrusted  = errors.New("设备尚未配对或信任已撤销")
	ErrGeneration = errors.New("设备信任已变化，请重新配对")
	deviceName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
)

// Document is the public identity exchanged during pairing. Name is a display label.
type Document struct {
	Version   int    `json:"version"`
	DeviceID  string `json:"device_id"`
	Name      string `json:"name"`
	PublicKey []byte `json:"public_key"`
}

func (i *Identity) Document(name string) Document {
	public, _ := x509.MarshalPKIXPublicKey(i.privateKey.Public())
	return Document{Version: Version, DeviceID: i.ID, Name: name, PublicKey: public}
}

func (d Document) Validate() error {
	if d.Version != Version || !deviceName.MatchString(d.Name) {
		return fmt.Errorf("设备配对文档版本或名称无效")
	}
	key, err := x509.ParsePKIXPublicKey(d.PublicKey)
	if err != nil {
		return err
	}
	id, err := IDFromKey(key)
	if err != nil || id != d.DeviceID {
		return fmt.Errorf("设备 ID 与公钥不一致")
	}
	return nil
}

// Trust grants application roles to one public key, on every network path.
type Trust struct {
	Document   Document   `json:"identity"`
	Alias      string     `json:"alias"`
	Tags       []string   `json:"tags"`
	Generation uint64     `json:"generation"`
	Revoked    bool       `json:"revoked"`
	Hints      []PathHint `json:"path_hints,omitempty"`
}

type trustFile struct {
	Version  int              `json:"version"`
	Revision uint64           `json:"revision"`
	Peers    map[string]Trust `json:"peers"`
}

// TrustStore serializes persistent trust changes and broadcasts their revision to each subscriber.
type TrustStore struct {
	mu          sync.RWMutex
	dir         string
	state       trustFile
	subscribers map[uint64]chan uint64
	next        uint64
}

func OpenTrust(stateDir string) (*TrustStore, error) {
	dir := filepath.Join(stateDir, "identity-v2")
	if err := privdir.Ensure(dir); err != nil {
		return nil, err
	}
	s := &TrustStore{dir: dir, subscribers: map[uint64]chan uint64{}}
	state, err := s.read()
	if err != nil {
		return nil, err
	}
	s.state = state
	return s, nil
}

func (s *TrustStore) read() (trustFile, error) {
	state := trustFile{Version: Version, Peers: map[string]Trust{}}
	raw, err := readPrivate(filepath.Join(s.dir, "trust.json"), 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if state.Version != Version || state.Peers == nil {
		return state, fmt.Errorf("设备信任文件版本无效")
	}
	aliases := map[string]bool{}
	for id, trust := range state.Peers {
		if err := trust.Document.Validate(); err != nil {
			return state, err
		}
		if id != trust.Document.DeviceID || !deviceName.MatchString(trust.Alias) || aliases[trust.Alias] || trust.Generation == 0 {
			return state, fmt.Errorf("设备信任文件记录无效")
		}
		if err := validateTags(trust.Tags); err != nil {
			return state, err
		}
		if len(trust.Hints) > 8 {
			return state, fmt.Errorf("设备连接提示过多")
		}
		for _, hint := range trust.Hints {
			if err := hint.Validate(); err != nil {
				return state, err
			}
		}
		aliases[trust.Alias] = true
	}
	return state, nil
}

func validateTags(tags []string) error {
	for _, tag := range tags {
		if !slices.Contains([]string{"tag:peer", "tag:device", "tag:admin", "tag:ingress"}, tag) {
			return fmt.Errorf("设备角色无效")
		}
	}
	return nil
}

func cloneTrust(t Trust) Trust {
	t.Hints = slices.Clone(t.Hints)
	t.Document.PublicKey = slices.Clone(t.Document.PublicKey)
	t.Tags = slices.Clone(t.Tags)
	return t
}

func (s *TrustStore) Revision() uint64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.state.Revision }

// Generations includes revocation tombstones, so a pairing started before revocation can
// distinguish an absent record from a revoked one even when the display list omits both.
func (s *TrustStore) Generations() map[string]uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]uint64, len(s.state.Peers))
	for id, t := range s.state.Peers {
		result[id] = t.Generation
	}
	return result
}

func (s *TrustStore) Find(id string) (Trust, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.state.Peers[id]
	return cloneTrust(t), ok && !t.Revoked
}

func (s *TrustStore) Snapshot() []Trust {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Trust, 0, len(s.state.Peers))
	for _, t := range s.state.Peers {
		if !t.Revoked {
			result = append(result, cloneTrust(t))
		}
	}
	slices.SortFunc(result, func(a, b Trust) int { return strings.Compare(a.Document.DeviceID, b.Document.DeviceID) })
	return result
}

// Subscribe returns an initial revision and coalesces later changes for this subscriber only.
func (s *TrustStore) Subscribe() (<-chan uint64, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	ch := make(chan uint64, 1)
	ch <- s.state.Revision
	s.subscribers[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if current, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(current)
		}
	}
}

func (s *TrustStore) adopt(state trustFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.Revision <= s.state.Revision {
		return
	}
	s.state = state
	for _, ch := range s.subscribers {
		select {
		case <-ch:
		default:
		}
		ch <- state.Revision
	}
}

// Refresh adopts changes from a completed offline transaction.
func (s *TrustStore) Refresh() error {
	state, err := s.read()
	if err != nil {
		return err
	}
	s.adopt(state)
	return nil
}

func (s *TrustStore) update(ctx context.Context, fn func(*trustFile) error) error {
	lock, err := filelock.Acquire(ctx, filepath.Join(s.dir, "trust.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	// Each writer re-reads under the file lock; independent instances cannot overwrite each other.
	state, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(&state); err != nil {
		return err
	}
	state.Revision++
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.dir, "trust.json"), raw); err != nil {
		return err
	}
	s.adopt(state)
	return nil
}

// Grant applies a locally approved pairing or role edit. expected is this device's generation,
// including its revocation tombstone. Unrelated devices can complete pairing concurrently.
// nil is reserved for explicit local administrative changes.
func (s *TrustStore) Grant(ctx context.Context, doc Document, tags []string, expected *uint64) error {
	if err := doc.Validate(); err != nil {
		return err
	}
	if err := validateTags(tags); err != nil {
		return err
	}
	doc.PublicKey = slices.Clone(doc.PublicKey)
	tags = slices.Compact(slices.Sorted(slices.Values(tags)))
	if tags == nil {
		tags = []string{}
	}
	return s.update(ctx, func(state *trustFile) error {
		previous, exists := state.Peers[doc.DeviceID]
		if expected != nil && previous.Generation != *expected {
			return ErrGeneration
		}
		alias := previous.Alias
		if !exists {
			alias = doc.Name
			used := func(alias string) bool {
				for id, p := range state.Peers {
					if id != doc.DeviceID && p.Alias == alias {
						return true
					}
				}
				return false
			}
			for n := 8; used(alias); n += 2 {
				if n > 60 {
					return fmt.Errorf("无法为设备分配唯一名称")
				}
				alias = doc.Name[:min(len(doc.Name), 62-n)] + "-" + doc.DeviceID[4:4+n]
			}
		}
		var hints []PathHint
		if !previous.Revoked {
			hints = slices.Clone(previous.Hints)
		}
		state.Peers[doc.DeviceID] = Trust{Document: doc, Alias: alias, Tags: tags, Generation: previous.Generation + 1, Hints: hints}
		return nil
	})
}

// SetTags replaces the roles of an already trusted device without re-pairing. Adding roles keeps
// the generation, so live logical sessions survive and the next session open or resume sees the
// new roles. Removing a role bumps the generation, which ends every session that was authorized
// under the broader set.
func (s *TrustStore) SetTags(ctx context.Context, id string, tags []string) (Trust, error) {
	if err := validateTags(tags); err != nil {
		return Trust{}, err
	}
	tags = slices.Compact(slices.Sorted(slices.Values(tags)))
	if tags == nil {
		tags = []string{}
	}
	var updated Trust
	err := s.update(ctx, func(state *trustFile) error {
		t, ok := state.Peers[id]
		if !ok || t.Revoked {
			return ErrUntrusted
		}
		if slices.Equal(t.Tags, tags) {
			updated = cloneTrust(t)
			return nil
		}
		for _, old := range t.Tags {
			if !slices.Contains(tags, old) {
				t.Generation++
				break
			}
		}
		t.Tags = tags
		state.Peers[id] = t
		updated = cloneTrust(t)
		return nil
	})
	return updated, err
}

func (s *TrustStore) Revoke(ctx context.Context, id string) error {
	return s.update(ctx, func(state *trustFile) error {
		t, ok := state.Peers[id]
		if !ok {
			return ErrUntrusted
		}
		t.Revoked, t.Generation = true, t.Generation+1
		state.Peers[id] = t
		return nil
	})
}

// VerifyCertificate checks the same pinned public identity on LAN and Tailnet. Path metadata,
// certificate serial numbers, display names and control-plane roles cannot grant device trust.
func (s *TrustStore) VerifyCertificate(cert *x509.Certificate, expected string) (Trust, error) {
	if cert == nil {
		return Trust{}, ErrUntrusted
	}
	id, err := IDFromKey(cert.PublicKey)
	if err != nil {
		return Trust{}, err
	}
	t, ok := s.Find(id)
	if !ok || (expected != "" && expected != id) {
		return Trust{}, ErrUntrusted
	}
	if cert.Subject.CommonName != id || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return Trust{}, fmt.Errorf("设备证书身份或有效期无效")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return Trust{}, err
	}
	return t, nil
}

func (s *TrustStore) TLS(i *Identity, expected string, server bool) *tls.Config {
	cfg := i.tlsConfig()
	if server {
		cfg.ClientAuth = tls.RequireAnyClientCert
	} else {
		cfg.InsecureSkipVerify = true
	} // Public-key pinning is performed below.
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 {
			return ErrUntrusted
		}
		_, err := s.VerifyCertificate(cs.PeerCertificates[0], expected)
		return err
	}
	return cfg
}
