package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Certificate struct {
	Certificate tls.Certificate
	Leaf        *x509.Certificate
	PEM         []byte
	Fingerprint string
}
type Trusted struct {
	Name           string   `json:"name"`
	Fingerprint    string   `json:"fingerprint"`
	CertificatePEM string   `json:"certificate_pem"`
	Tags           []string `json:"tags"`
}
type TrustStore struct {
	mu    sync.RWMutex
	dir   string
	peers map[string]Trusted
}

func LoadCertificate(dir, name string) (Certificate, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return Certificate{}, e
	}
	certPath, keyPath := filepath.Join(dir, "node.pem"), filepath.Join(dir, "node.key")
	certPEM, ce := os.ReadFile(certPath)
	keyPEM, ke := os.ReadFile(keyPath)
	if os.IsNotExist(ce) && os.IsNotExist(ke) {
		public, private, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return Certificate{}, e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return Certificate{}, e
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
		der, e := x509.CreateCertificate(rand.Reader, template, template, public, private)
		if e != nil {
			return Certificate{}, e
		}
		key, e := x509.MarshalPKCS8PrivateKey(private)
		if e != nil {
			return Certificate{}, e
		}
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
		if e = writePrivate(keyPath, keyPEM); e != nil {
			return Certificate{}, e
		}
		if e = writePrivate(certPath, certPEM); e != nil {
			return Certificate{}, e
		}
	} else if ce != nil || ke != nil {
		return Certificate{}, fmt.Errorf("LAN 节点证书不完整")
	}
	cert, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		return Certificate{}, e
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil {
		return Certificate{}, e
	}
	if leaf.Subject.CommonName != name {
		return Certificate{}, fmt.Errorf("证书节点名与配置不一致")
	}
	hash := sha256.Sum256(leaf.Raw)
	return Certificate{cert, leaf, certPEM, hex.EncodeToString(hash[:])}, nil
}
func writePrivate(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".xmesh-state-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func OpenTrust(dir string) (*TrustStore, error) {
	s := &TrustStore{dir: dir, peers: map[string]Trusted{}}
	b, e := os.ReadFile(filepath.Join(dir, "peers.json"))
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &s.peers); e != nil {
		return nil, e
	}
	for fp, p := range s.peers {
		if e := validateTrusted(p); e != nil || fp != p.Fingerprint {
			return nil, fmt.Errorf("LAN 信任库校验失败")
		}
	}
	return s, nil
}
func validateTrusted(p Trusted) error {
	block, _ := pem.Decode([]byte(p.CertificatePEM))
	if block == nil {
		return fmt.Errorf("证书 PEM 无效")
	}
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return e
	}
	hash := sha256.Sum256(cert.Raw)
	if p.Fingerprint != hex.EncodeToString(hash[:]) || p.Name != cert.Subject.CommonName {
		return fmt.Errorf("证书与指纹或节点名不一致")
	}
	if strings.ContainsAny(p.Name, "/\\:") {
		return fmt.Errorf("节点名无效")
	}
	return nil
}
func (s *TrustStore) Add(p Trusted) error {
	if e := validateTrusted(p); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := map[string]Trusted{}
	for k, v := range s.peers {
		updated[k] = v
	}
	updated[p.Fingerprint] = p
	b, e := json.MarshalIndent(updated, "", "  ")
	if e != nil {
		return e
	}
	if e = writePrivate(filepath.Join(s.dir, "peers.json"), b); e != nil {
		return e
	}
	s.peers = updated
	return nil
}
func (s *TrustStore) Find(fp string) (Trusted, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[fp]
	return p, ok
}
func fingerprint(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(h[:])
}
func (s *TrustStore) TLS(cert Certificate, expected string, server bool) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert.Certificate}}
	if server {
		cfg.ClientAuth = tls.RequireAnyClientCert
	} else {
		cfg.InsecureSkipVerify = true
	} // Verification is the pinned certificate check below.
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 {
			return fmt.Errorf("需要已配对的节点证书")
		}
		peer := cs.PeerCertificates[0]
		fp := fingerprint(peer)
		trusted, ok := s.Find(fp)
		if !ok || (expected != "" && fp != expected) {
			return fmt.Errorf("对端尚未配对")
		}
		if trusted.Name != peer.Subject.CommonName || time.Now().Before(peer.NotBefore) || time.Now().After(peer.NotAfter) {
			return fmt.Errorf("对端证书无效或过期")
		}
		return peer.CheckSignature(peer.SignatureAlgorithm, peer.RawTBSCertificate, peer.Signature)
	}
	return cfg
}
