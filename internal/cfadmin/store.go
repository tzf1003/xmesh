package cfadmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

// ErrNoCredential means nothing is stored for the profile.
var ErrNoCredential = errors.New("cfadmin: no stored Cloudflare credential")

// errCorruptCredential marks a stored credential that cannot be parsed, as opposed to a keychain that
// is simply unavailable.
var errCorruptCredential = errors.New("cfadmin: corrupt stored credential")

// KeychainService is the service name in the operating system's secret store.
const KeychainService = "xmesh-cloudflare"

// Credential is what the commands need to manage one mesh's Cloudflare resources.
type Credential struct {
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
	ZoneID    string `json:"zone_id"`
	Domain    string `json:"domain"`
}

func (c Credential) String() string {
	return "Credential{account=" + c.AccountID + " zone=" + c.ZoneID + " domain=" + c.Domain + " token=<redacted>}"
}
func (c Credential) GoString() string { return c.String() }

func isID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Validate checks the shape of every field. Cloudflare account and zone IDs are 32 hex characters.
func (c Credential) Validate() error {
	switch {
	case strings.TrimSpace(c.Token) == "" || strings.ContainsAny(c.Token, " \r\n\t"):
		return errors.New("Cloudflare API 令牌为空或含空白")
	case !isID(c.AccountID):
		return errors.New("Cloudflare 账号 ID 应为 32 位十六进制")
	case !isID(c.ZoneID):
		return errors.New("Cloudflare 区域 ID 应为 32 位十六进制")
	case !domainPattern.MatchString(c.Domain):
		return errors.New("Cloudflare 域名无效，应为小写域名")
	}
	return nil
}

// Client builds the API client for this credential.
func (c Credential) Client() *Client {
	return &Client{Token: c.Token, AccountID: c.AccountID, ZoneID: c.ZoneID}
}

// Store keeps the credential outside xmesh.yaml and outside the kernel.
type Store interface {
	Get(profile string) (Credential, error)
	Put(profile string, c Credential) error
	Delete(profile string) error
}

// CheckProfile rejects a profile name that could escape the directory it is used to name.
func CheckProfile(profile string) error {
	if profile == "" || strings.ContainsAny(profile, "/\\\x00\r\n") {
		return fmt.Errorf("配置名 %q 无效", profile)
	}
	return nil
}

// KeychainStore uses the operating system's secret store.
type KeychainStore struct{}

func (KeychainStore) Get(profile string) (Credential, error) {
	if err := CheckProfile(profile); err != nil {
		return Credential{}, err
	}
	s, err := keyring.Get(KeychainService, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credential{}, fmt.Errorf("%w: %q", ErrNoCredential, profile)
	}
	if err != nil {
		return Credential{}, fmt.Errorf("读取系统钥匙串失败：%w", err)
	}
	var c Credential
	if json.Unmarshal([]byte(s), &c) != nil {
		return Credential{}, fmt.Errorf("%w：钥匙串中 %q 的 Cloudflare 凭据已损坏", errCorruptCredential, profile)
	}
	return c, nil
}

func (KeychainStore) Put(profile string, c Credential) error {
	if err := CheckProfile(profile); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(c)
	if err := keyring.Set(KeychainService, profile, string(raw)); err != nil {
		return fmt.Errorf("写入系统钥匙串失败：%w", err)
	}
	return nil
}

func (KeychainStore) Delete(profile string) error {
	if err := CheckProfile(profile); err != nil {
		return err
	}
	if err := keyring.Delete(KeychainService, profile); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("删除钥匙串条目失败：%w", err)
	}
	return nil
}

// FileStore is for hosts with no secret store. The file is private to the owner.
type FileStore struct{ Path string }

func (s FileStore) read() (Credential, error) {
	info, err := os.Stat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Credential{}, fmt.Errorf("凭据文件 %s 权限过宽，应为 0600", s.Path)
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if json.Unmarshal(raw, &c) != nil {
		return Credential{}, errors.New("Cloudflare 凭据文件已损坏")
	}
	return c, nil
}

func (s FileStore) Get(string) (Credential, error) { return s.read() }

func (s FileStore) Put(_ string, c Credential) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(c)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

func (s FileStore) Delete(string) error {
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// MemoryStore is for tests and one-shot callers.
type MemoryStore struct{ m map[string]Credential }

func (s *MemoryStore) Get(profile string) (Credential, error) {
	c, ok := s.m[profile]
	if !ok {
		return Credential{}, ErrNoCredential
	}
	return c, nil
}

func (s *MemoryStore) Put(profile string, c Credential) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if s.m == nil {
		s.m = map[string]Credential{}
	}
	s.m[profile] = c
	return nil
}

func (s *MemoryStore) Delete(profile string) error { delete(s.m, profile); return nil }

// FallbackStore uses the system keychain and falls back to a private file under Dir when the
// host has none, such as a server without a desktop session.
type FallbackStore struct {
	Primary Store
	// Dir holds one <profile>.credential.json per profile.
	Dir string
}

func (s FallbackStore) file(profile string) (FileStore, error) {
	if err := CheckProfile(profile); err != nil {
		return FileStore{}, err
	}
	if s.Dir == "" {
		return FileStore{}, errors.New("无法确定凭据文件目录")
	}
	return FileStore{Path: filepath.Join(s.Dir, profile+".credential.json")}, nil
}

// Get prefers the fallback file: it exists only while it is newer than anything in the keychain, because
// a successful keychain write removes it.
func (s FallbackStore) Get(profile string) (Credential, error) {
	f, ferr := s.file(profile)
	if ferr == nil {
		c, err := f.Get(profile)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, ErrNoCredential) {
			return Credential{}, err
		}
	}
	c, err := s.Primary.Get(profile)
	switch {
	case err == nil:
		return c, nil
	case errors.Is(err, errCorruptCredential):
		return Credential{}, err
	case errors.Is(err, ErrNoCredential):
		return Credential{}, err
	}
	// The keychain is unavailable and no file was saved: nothing is stored yet, so a first login can proceed.
	return Credential{}, fmt.Errorf("%w（系统钥匙串不可用：%v）", ErrNoCredential, err)
}

func (s FallbackStore) Put(profile string, c Credential) error {
	if err := CheckProfile(profile); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	f, ferr := s.file(profile)
	perr := s.Primary.Put(profile, c)
	if perr == nil {
		// A stale fallback file would win over the new keychain entry, so a failed removal is an error.
		if ferr == nil {
			if err := f.Delete(profile); err != nil {
				return fmt.Errorf("已写入钥匙串，但未能删除旧的凭据文件 %s：%w", f.Path, err)
			}
		}
		return nil
	}
	if ferr != nil {
		return perr
	}
	if err := f.Put(profile, c); err != nil {
		return fmt.Errorf("%w；改存凭据文件也失败：%v", perr, err)
	}
	return nil
}

// Delete removes the file and the keychain entry. A keychain that is unavailable cannot hold a credential
// this store wrote, so it only fails the call when there was no file to remove.
func (s FallbackStore) Delete(profile string) error {
	err := s.Primary.Delete(profile)
	f, ferr := s.file(profile)
	if ferr != nil {
		return err
	}
	_, statErr := os.Stat(f.Path)
	fileExisted := statErr == nil
	derr := f.Delete(profile)
	switch {
	case derr != nil:
		return derr
	case err != nil && fileExisted:
		return nil
	}
	return err
}
