package device

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/spake2"
)

const PairALPN = "xmesh-pair/2"
const PairPath = "/mesh/v2/pair/"
const pairTTL = time.Minute
const pairLimit = 16

var (
	ErrPairDisabled     = errors.New("设备尚未设置连接密码")
	ErrPairPassword     = errors.New("连接密码长度须为 10 到 128 个字符，首尾不能有空白")
	ErrPairLimited      = errors.New("配对尝试过多，请稍后重试")
	ErrPairProof        = errors.New("连接密码或对端设备身份无效")
	ErrIdentityMismatch = errors.New("对端设备公钥指纹与所选设备不一致")
	ErrPairUnsupported  = errors.New("对端设备协议版本不兼容，请更新并重新配对")
)

type passwordRecord struct {
	Version   int       `json:"version"`
	Salt      []byte    `json:"salt"`
	Key       []byte    `json:"key"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PairStatus struct {
	Set            bool
	UpdatedAt      time.Time
	FailedAttempts int
	LockedUntil    time.Time
}

type pairExchange struct {
	responder                 *spake2.Responder
	document                  Document
	generation, trustRevision uint64
	created                   time.Time
}

type pairReceipt struct {
	proof           [32]byte
	deviceID        string
	trustGeneration uint64
	created         time.Time
}

// Pairing owns one password generation and its transactions for all network listeners.
type Pairing struct {
	mu         sync.Mutex
	identity   *Identity
	document   Document
	trust      *TrustStore
	dir        string
	record     *passwordRecord
	generation uint64
	pending    map[string]pairExchange
	completed  map[string]pairReceipt
	failures   int
	until      time.Time
}

func OpenPairing(identity *Identity, name string, trust *TrustStore) (*Pairing, error) {
	doc := identity.Document(name)
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	p := &Pairing{identity: identity, document: doc, trust: trust, dir: trust.dir, pending: map[string]pairExchange{}, completed: map[string]pairReceipt{}}
	raw, err := readPrivate(filepath.Join(p.dir, "pair-password.json"), 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var rec passwordRecord
	if json.Unmarshal(raw, &rec) != nil || rec.Version != Version || len(rec.Salt) != spake2.SaltSize || len(rec.Key) != spake2.KeySize {
		return nil, fmt.Errorf("设备连接密码文件无效")
	}
	p.record = &rec
	return p, nil
}

func ValidatePassword(password string) error {
	if n := utf8.RuneCountInString(password); n < 10 || n > 128 || strings.TrimSpace(password) != password {
		return ErrPairPassword
	}
	return nil
}

func (p *Pairing) SetPassword(password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	salt := make([]byte, spake2.SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	record := &passwordRecord{Version: Version, Salt: salt, Key: spake2.Derive(password, salt), UpdatedAt: time.Now().UTC()}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := writeAtomic(filepath.Join(p.dir, "pair-password.json"), raw); err != nil {
		return err
	}
	p.record = record
	p.resetLocked()
	return nil
}

func (p *Pairing) ClearPassword() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.Remove(filepath.Join(p.dir, "pair-password.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p.record = nil
	p.resetLocked()
	return nil
}

func (p *Pairing) resetLocked() {
	p.generation++
	p.pending = map[string]pairExchange{}
	p.completed = map[string]pairReceipt{}
	p.failures, p.until = 0, time.Time{}
}

func (p *Pairing) Status() PairStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := PairStatus{Set: p.record != nil, FailedAttempts: p.failures, LockedUntil: p.until}
	if p.record != nil {
		st.UpdatedAt = p.record.UpdatedAt
	}
	return st
}

type PairChallenge struct {
	Version int    `json:"version"`
	Salt    []byte `json:"salt"`
	KDF     string `json:"kdf"`
}
type PairRequest struct {
	Identity Document `json:"identity"`
	X        []byte   `json:"x"`
}
type PairReply struct {
	SID      string   `json:"sid"`
	Identity Document `json:"identity"`
	Y        []byte   `json:"y"`
	Confirm  []byte   `json:"confirm"`
}
type PairConfirmation struct {
	SID     string `json:"sid"`
	Confirm []byte `json:"confirm"`
}

func (p *Pairing) Challenge() (PairChallenge, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.record == nil {
		return PairChallenge{}, ErrPairDisabled
	}
	return PairChallenge{Version: Version, Salt: append([]byte(nil), p.record.Salt...), KDF: "argon2id-v1"}, nil
}

func pairTranscript(doc Document) []byte {
	raw, _ := json.Marshal(doc)
	return append([]byte("xmesh-device-pair/v2\x00"), raw...)
}

func (p *Pairing) Exchange(req PairRequest) (PairReply, error) {
	if req.Identity.Validate() != nil || req.Identity.DeviceID == p.identity.ID {
		return PairReply{}, ErrPairProof
	}
	p.mu.Lock()
	if p.record == nil {
		p.mu.Unlock()
		return PairReply{}, ErrPairDisabled
	}
	if time.Now().Before(p.until) {
		p.mu.Unlock()
		return PairReply{}, ErrPairLimited
	}
	p.failures++
	if p.failures >= 5 {
		p.until = time.Now().Add(min(30*time.Second*time.Duration(uint64(1)<<min(p.failures-5, 6)), 30*time.Minute))
	}
	key := append([]byte(nil), p.record.Key...)
	generation, revision := p.generation, p.trust.Generations()[req.Identity.DeviceID]
	p.mu.Unlock()
	secret, err := spake2.NewSecret(key)
	if err != nil {
		return PairReply{}, err
	}
	responder := spake2.NewResponder(secret, pairTranscript(p.document))
	y, confirm, err := responder.Reply(req.X, pairTranscript(req.Identity))
	if err != nil {
		return PairReply{}, ErrPairProof
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation || p.record == nil {
		return PairReply{}, ErrGeneration
	}
	p.expireLocked()
	if len(p.pending) >= pairLimit {
		return PairReply{}, ErrPairLimited
	}
	sid := rand.Text()
	p.pending[sid] = pairExchange{responder: responder, document: req.Identity, generation: generation, trustRevision: revision, created: time.Now()}
	return PairReply{SID: sid, Identity: p.document, Y: y, Confirm: confirm}, nil
}

func (p *Pairing) expireLocked() {
	for sid, exchange := range p.pending {
		if time.Since(exchange.created) > pairTTL {
			delete(p.pending, sid)
		}
	}
	for sid, receipt := range p.completed {
		if time.Since(receipt.created) > pairTTL {
			delete(p.completed, sid)
		}
	}
}

func (p *Pairing) Confirm(ctx context.Context, req PairConfirmation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked()
	proof := sha256.Sum256(req.Confirm)
	if receipt, ok := p.completed[req.SID]; ok {
		trusted, valid := p.trust.Find(receipt.deviceID)
		if !valid || trusted.Generation != receipt.trustGeneration || subtle.ConstantTimeCompare(proof[:], receipt.proof[:]) != 1 {
			return ErrPairProof
		}
		return nil
	}
	exchange, ok := p.pending[req.SID]
	if !ok || p.record == nil || exchange.generation != p.generation {
		return ErrGeneration
	}
	if err := exchange.responder.Confirm(req.Confirm); err != nil {
		delete(p.pending, req.SID)
		return ErrPairProof
	}
	tags := []string{"tag:peer", "tag:device"}
	if existing, ok := p.trust.Find(exchange.document.DeviceID); ok {
		tags = existing.Tags
	}
	// The password lock remains held through the conditional trust transaction.
	if err := p.trust.Grant(ctx, exchange.document, tags, &exchange.trustRevision); err != nil {
		delete(p.pending, req.SID)
		return err
	}
	delete(p.pending, req.SID)
	t, _ := p.trust.Find(exchange.document.DeviceID)
	if len(p.completed) >= pairLimit {
		for sid := range p.completed {
			delete(p.completed, sid)
			break
		}
	}
	p.completed[req.SID] = pairReceipt{proof: proof, deviceID: exchange.document.DeviceID, trustGeneration: t.Generation, created: time.Now()}
	p.failures, p.until = 0, time.Time{}
	return nil
}

// ServerTLS permits untrusted certificates only for the isolated pairing protocol.
func (p *Pairing) ServerTLS() *tls.Config {
	strict := p.trust.TLS(p.identity, "", true)
	strict.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		for _, protocol := range hello.SupportedProtos {
			if protocol == PairALPN {
				return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{p.identity.Certificate}, ClientAuth: tls.RequestClientCert}, nil
			}
		}
		return nil, nil
	}
	return strict
}

// BootstrapTLS authenticates possession of the peer key; the password transcript and
// optional expected device ID establish which device the user is approving.
func (p *Pairing) BootstrapTLS() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
		Certificates: []tls.Certificate{p.identity.Certificate}, NextProtos: []string{PairALPN},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) != 1 {
				return ErrPairProof
			}
			return verifyBootstrapCertificate(cs.PeerCertificates[0])
		},
	}
}

func verifyBootstrapCertificate(cert *x509.Certificate) error {
	id, err := IDFromKey(cert.PublicKey)
	if err != nil || cert.Subject.CommonName != id || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return ErrPairProof
	}
	return cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature)
}

// Handler shares pairing transactions across LAN and Tailnet, restricting bootstrap requests to
// the pairing routes. Application handlers must revalidate trust on each request as well.
func (p *Pairing) Handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PairPath+"identity", func(w http.ResponseWriter, r *http.Request) { pairResponse(w, p.document, nil) })
	mux.HandleFunc("POST "+PairPath+"challenge", func(w http.ResponseWriter, r *http.Request) { out, err := p.Challenge(); pairResponse(w, out, err) })
	mux.HandleFunc("POST "+PairPath+"exchange", func(w http.ResponseWriter, r *http.Request) {
		var req PairRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			pairResponse(w, nil, ErrPairProof)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
			pairResponse(w, nil, ErrPairProof)
			return
		}
		cert := r.TLS.PeerCertificates[0]
		id, err := IDFromKey(cert.PublicKey)
		if err != nil || id != req.Identity.DeviceID || verifyBootstrapCertificate(cert) != nil {
			pairResponse(w, nil, ErrPairProof)
			return
		}
		out, err := p.Exchange(req)
		pairResponse(w, out, err)
	})
	mux.HandleFunc("POST "+PairPath+"confirm", func(w http.ResponseWriter, r *http.Request) {
		var req PairConfirmation
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			pairResponse(w, nil, ErrPairProof)
			return
		}
		pairResponse(w, struct{}{}, p.Confirm(r.Context(), req))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, PairPath) {
			mux.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
			http.NotFound(w, r)
			return
		}
		if _, err := p.trust.VerifyCertificate(r.TLS.PeerCertificates[0], ""); err != nil {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func pairResponse(w http.ResponseWriter, out any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status, code := http.StatusBadRequest, "pair_rejected"
		switch {
		case errors.Is(err, ErrPairDisabled):
			status, code = 409, "pair_disabled"
		case errors.Is(err, ErrPairLimited):
			status, code = 429, "rate_limited"
		case errors.Is(err, ErrGeneration):
			status, code = 409, "generation_changed"
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

// PairWith proves the password over the supplied bootstrap-only HTTP client. It works with an
// OS or tsnet dialer. expected is the device ID the user selected, when known.
func (p *Pairing) PairWith(ctx context.Context, client *http.Client, base, password, expected string) (Document, error) {
	if err := ValidatePassword(password); err != nil {
		return Document{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &copyClient
	var tlsIdentity string
	generations := p.trust.Generations()
	var challenge PairChallenge
	if err := pairPOST(ctx, client, base+PairPath+"challenge", struct{}{}, &challenge, &tlsIdentity); err != nil {
		return Document{}, err
	}
	if challenge.Version != Version || challenge.KDF != "argon2id-v1" || len(challenge.Salt) != spake2.SaltSize {
		return Document{}, ErrPairUnsupported
	}
	secret, err := spake2.NewSecret(spake2.Derive(password, challenge.Salt))
	if err != nil {
		return Document{}, err
	}
	initiator, x, err := spake2.NewInitiator(secret, pairTranscript(p.document))
	if err != nil {
		return Document{}, err
	}
	var reply PairReply
	if err := pairPOST(ctx, client, base+PairPath+"exchange", PairRequest{Identity: p.document, X: x}, &reply, &tlsIdentity); err != nil {
		return Document{}, err
	}
	if reply.Identity.Validate() != nil || reply.Identity.DeviceID != tlsIdentity || reply.Identity.DeviceID == p.identity.ID {
		return Document{}, ErrPairProof
	}
	if expected != "" && expected != reply.Identity.DeviceID {
		return Document{}, ErrIdentityMismatch
	}
	confirm, err := initiator.Finish(reply.Y, pairTranscript(reply.Identity), reply.Confirm)
	if err != nil {
		return Document{}, ErrPairProof
	}
	for attempt := 0; ; attempt++ {
		err := pairPOST(ctx, client, base+PairPath+"confirm", PairConfirmation{SID: reply.SID, Confirm: confirm}, &struct{}{}, &tlsIdentity)
		if err == nil {
			break
		}
		if attempt >= 1 || ctx.Err() != nil || errors.Is(err, ErrPairProof) || errors.Is(err, ErrGeneration) || errors.Is(err, ErrPairLimited) {
			return Document{}, err
		}
	}
	tags := []string{"tag:peer", "tag:device"}
	if old, ok := p.trust.Find(reply.Identity.DeviceID); ok {
		tags = old.Tags
	}
	generation := generations[reply.Identity.DeviceID]
	if err := p.trust.Grant(ctx, reply.Identity, tags, &generation); err != nil {
		return Document{}, err
	}
	return reply.Identity, nil
}

func pairPOST(ctx context.Context, client *http.Client, url string, in, out any, tlsIdentity *string) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.TLS == nil || len(response.TLS.PeerCertificates) != 1 {
		return ErrPairProof
	}
	peer := response.TLS.PeerCertificates[0]
	id, err := IDFromKey(peer.PublicKey)
	if err != nil || verifyBootstrapCertificate(peer) != nil || (*tlsIdentity != "" && *tlsIdentity != id) {
		return ErrPairProof
	}
	*tlsIdentity = id
	raw, err = io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return ErrPairProof
	}
	if response.StatusCode != 200 {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.Error == "pair_disabled" {
			return ErrPairDisabled
		}
		if response.StatusCode == 404 || response.StatusCode == 426 {
			return ErrPairUnsupported
		}
		switch response.StatusCode {
		case 429:
			return ErrPairLimited
		case 409:
			return ErrGeneration
		}
		return ErrPairProof
	}
	return json.Unmarshal(raw, out)
}
