// Package device owns the persistent xmesh identity and device trust, independently of network paths.
package device

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
)

const Version = 2

// Identity is one installation's key. The certificate can be renewed without changing ID.
type Identity struct {
	ID          string
	Certificate tls.Certificate
	privateKey  ed25519.PrivateKey
}

type identityFile struct {
	Version     int    `json:"version"`
	PrivateKey  []byte `json:"private_key"`
	Certificate []byte `json:"certificate"`
}

// IDFromKey binds the complete canonical public key to its device ID.
func IDFromKey(public any) (string, error) {
	key, ok := public.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return "", fmt.Errorf("设备身份需要 Ed25519 公钥")
	}
	spki, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(spki)
	return "dev-" + hex.EncodeToString(sum[:]), nil
}

// NewIdentity creates a fresh identity. LoadIdentity persists identities used by services.
func NewIdentity() (*Identity, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return identityForKey(key, time.Now())
}

func identityForKey(key ed25519.PrivateKey, now time.Time) (*Identity, error) {
	id, err := IDFromKey(key.Public())
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: id},
		NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Identity{ID: id, privateKey: key, Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}, nil
}

// LoadIdentity creates or loads identity-v2/identity.json under stateDir. The single atomic
// record keeps a concurrent first start or an interrupted write from separating the key and cert.
func LoadIdentity(ctx context.Context, stateDir string) (*Identity, error) {
	dir := filepath.Join(stateDir, "identity-v2")
	if err := privdir.Ensure(dir); err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(ctx, filepath.Join(dir, "identity.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	path := filepath.Join(dir, "identity.json")
	raw, err := readPrivate(path, 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		id, err := NewIdentity()
		if err != nil {
			return nil, err
		}
		return id, saveIdentity(path, id)
	}
	if err != nil {
		return nil, err
	}
	var record identityFile
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("设备身份文件无效: %w", err)
	}
	if record.Version != Version {
		return nil, fmt.Errorf("设备身份版本不兼容")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(record.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("设备私钥无效: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("设备私钥类型无效")
	}
	leaf, err := x509.ParseCertificate(record.Certificate)
	if err != nil {
		return nil, err
	}
	id, err := IDFromKey(key.Public())
	if err != nil {
		return nil, err
	}
	certID, err := IDFromKey(leaf.PublicKey)
	if err != nil || certID != id || leaf.Subject.CommonName != id || leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
		return nil, fmt.Errorf("设备证书与私钥不一致")
	}
	identity := &Identity{ID: id, privateKey: key, Certificate: tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}}
	if time.Until(leaf.NotAfter) < 30*24*time.Hour || time.Now().Before(leaf.NotBefore) {
		identity, err = identityForKey(key, time.Now())
		if err != nil {
			return nil, err
		}
		if err := saveIdentity(path, identity); err != nil {
			return nil, err
		}
	}
	return identity, nil
}

func saveIdentity(path string, id *Identity) error {
	key, err := x509.MarshalPKCS8PrivateKey(id.privateKey)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(identityFile{Version, key, id.Certificate.Certificate[0]})
	if err != nil {
		return err
	}
	return writeAtomic(path, raw)
}

func readPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("设备状态文件权限或大小无效")
	}
	return os.ReadFile(path)
}

func writeAtomic(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".device-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
