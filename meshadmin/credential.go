package meshadmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// Access selects which of the two credentials of a profile to use. Reads use the
// read credential; a change uses the write credential, which is only fetched at
// that moment (RUL-22).
type Access string

const (
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

// CredType says what Secret holds.
type CredType string

const (
	CredOAuth  CredType = "oauth"  // Tailscale OAuth client: ClientID + Secret
	CredToken  CredType = "token"  // Tailscale API access token
	CredAPIKey CredType = "apikey" // Headscale API key
)

// Credential is everything needed to reach one control plane. Secret is the
// only sensitive field; the type refuses to print it.
type Credential struct {
	Access   Access   `json:"access,omitempty"`
	Kind     Kind     `json:"kind"`
	Type     CredType `json:"type"`
	URL      string   `json:"url,omitempty"`
	Tailnet  string   `json:"tailnet,omitempty"`
	ClientID string   `json:"client_id,omitempty"`
	Secret   string   `json:"secret"`
}

// String and GoString redact the secret so a stray %v or %#v cannot leak it.
func (c Credential) String() string {
	return fmt.Sprintf("Credential{kind=%s type=%s url=%s client_id=%s secret=<redacted>}", c.Kind, c.Type, c.URL, c.ClientID)
}

func (c Credential) GoString() string { return c.String() }

// Validate checks the combination of fields.
func (c Credential) Validate() error {
	if c.Access != "" && c.Access != AccessRead && c.Access != AccessWrite {
		return fmt.Errorf("%w: credential access", ErrInvalid)
	}
	if strings.TrimSpace(c.Secret) == "" {
		return fmt.Errorf("%w: credential has no secret", ErrInvalid)
	}
	switch c.Kind {
	case KindTailscale:
		switch c.Type {
		case CredOAuth:
			if c.ClientID == "" {
				return fmt.Errorf("%w: OAuth credential needs a client id", ErrInvalid)
			}
		case CredToken:
		default:
			return fmt.Errorf("%w: Tailscale credential type %q", ErrInvalid, c.Type)
		}
	case KindHeadscale:
		if c.Type != CredAPIKey {
			return fmt.Errorf("%w: Headscale credential type %q", ErrInvalid, c.Type)
		}
		if c.URL == "" {
			return fmt.Errorf("%w: Headscale credential needs the server URL", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: control plane kind %q", ErrInvalid, c.Kind)
	}
	return nil
}

// NewProvider builds the Provider for a credential. access only matters for
// Tailscale OAuth, where it picks the scopes the token request asks for.
func NewProvider(c Credential, access Access) (Provider, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch c.Kind {
	case KindTailscale:
		cfg := TailscaleConfig{BaseURL: c.URL, Tailnet: c.Tailnet}
		if c.Type == CredToken {
			cfg.Token = c.Secret
		} else {
			cfg.ClientID, cfg.ClientSecret = c.ClientID, c.Secret
			cfg.Scopes = TailscaleReadScopes
			if access == AccessWrite {
				cfg.Scopes = TailscaleWriteScopes
			}
		}
		return NewTailscale(cfg)
	default:
		return NewHeadscale(HeadscaleConfig{BaseURL: c.URL, APIKey: c.Secret})
	}
}

// ErrNoCredential means nothing is stored for the profile and access level.
var ErrNoCredential = errors.New("meshadmin: no stored credential")

// CredentialStore keeps credentials outside the kernel and outside xmesh.yaml.
type CredentialStore interface {
	Get(profile string, a Access) (Credential, error)
	Put(profile string, a Access, c Credential) error
	Delete(profile string, a Access) error
}

// KeychainService is the service name under which credentials are stored in the
// operating system's secret store (Windows Credential Manager, macOS Keychain,
// Secret Service on Linux).
const KeychainService = "xmesh-meshadmin"

// KeychainStore stores credentials in the OS secret store.
type KeychainStore struct{}

func account(profile string, a Access) (string, error) {
	if profile == "" || strings.ContainsAny(profile, "/\\\x00\r\n") {
		return "", fmt.Errorf("%w: profile name %q", ErrInvalid, profile)
	}
	if a != AccessRead && a != AccessWrite {
		return "", fmt.Errorf("%w: access %q", ErrInvalid, a)
	}
	return profile + "/" + string(a), nil
}

func (KeychainStore) Get(profile string, a Access) (Credential, error) {
	acct, err := account(profile, a)
	if err != nil {
		return Credential{}, err
	}
	s, err := keyring.Get(KeychainService, acct)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credential{}, fmt.Errorf("%w: profile %q (%s)", ErrNoCredential, profile, a)
	}
	if err != nil {
		return Credential{}, fmt.Errorf("meshadmin: read system keychain: %w", err)
	}
	var c Credential
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return Credential{}, fmt.Errorf("meshadmin: stored credential for %q is corrupt", profile)
	}
	return c, nil
}

func (KeychainStore) Put(profile string, a Access, c Credential) error {
	acct, err := account(profile, a)
	if err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(c)
	if err := keyring.Set(KeychainService, acct, string(b)); err != nil {
		if errors.Is(err, keyring.ErrSetDataTooBig) {
			return fmt.Errorf("meshadmin: credential too large for the system keychain")
		}
		return fmt.Errorf("meshadmin: write system keychain: %w", err)
	}
	return nil
}

func (KeychainStore) Delete(profile string, a Access) error {
	acct, err := account(profile, a)
	if err != nil {
		return err
	}
	if err := keyring.Delete(KeychainService, acct); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("meshadmin: delete from system keychain: %w", err)
	}
	return nil
}

// MemoryStore keeps credentials in process memory (tests, one-shot callers).
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Credential
}

func (s *MemoryStore) Get(profile string, a Access) (Credential, error) {
	acct, err := account(profile, a)
	if err != nil {
		return Credential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[acct]
	if !ok {
		return Credential{}, fmt.Errorf("%w: profile %q (%s)", ErrNoCredential, profile, a)
	}
	return c, nil
}

func (s *MemoryStore) Put(profile string, a Access, c Credential) error {
	acct, err := account(profile, a)
	if err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]Credential{}
	}
	s.m[acct] = c
	return nil
}

func (s *MemoryStore) Delete(profile string, a Access) error {
	acct, err := account(profile, a)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, acct)
	return nil
}

// ReadCredentialFile loads a credential JSON from a file for headless hosts that
// have no secret store. On Unix the file must not be readable by group or other.
func ReadCredentialFile(path string) (Credential, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Credential{}, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return Credential{}, fmt.Errorf("%w: %s must not be accessible by group or other (chmod 600)", ErrInvalid, path)
	}
	if st.Size() > 8192 {
		return Credential{}, fmt.Errorf("%w: %s is too large", ErrInvalid, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return Credential{}, fmt.Errorf("%w: %s is not a credential file", ErrInvalid, path)
	}
	return c, c.Validate()
}

// WriteCredentialFile stores c as JSON at path with mode 0600, replacing an
// existing file atomically. It is the file-backed alternative to the keychain
// for hosts whose desktop session cannot reach the OS secret store.
func WriteCredentialFile(path string, c Credential) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// Resolve returns the credential for an operation. Reads prefer the read
// credential and fall back to the write one; changes require the write one.
func Resolve(store CredentialStore, profile string, a Access) (Credential, Access, error) {
	c, err := store.Get(profile, a)
	if err == nil {
		return c, a, nil
	}
	if a == AccessRead && errors.Is(err, ErrNoCredential) {
		if w, werr := store.Get(profile, AccessWrite); werr == nil {
			return w, AccessWrite, nil
		}
	}
	return Credential{}, a, err
}
