package mesh

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/mdns"
	"github.com/tzf1003/xmesh/internal/spake2"
)

// Connection-password pairing (docs/design/14-password-pairing.md). A device stores a salt and an
// Argon2id key derived from its connection password. Another device types that password and runs
// SPAKE2 against the agent listener. On success both sides pin each other's certificate.

// PairALPN is the application protocol a pairing client offers in its handshake. A handshake that
// offers it gets the pairing TLS configuration, which does not check the client certificate. The
// server does not select it: net/http does not serve HTTP over an application protocol it does not
// know.
const PairALPN = "xmesh-pair/1"

// PairPath is the route prefix of the pairing protocol on the agent listener.
const PairPath = "/lan/pair/"

const (
	pairPasswordFile = "pair-password.json"
	pairKDF          = "argon2id-v1"
	pairMinRunes     = 10
	pairMaxRunes     = 128
	pairFreeAttempts = 5
	pairBackoffBase  = 30 * time.Second
	pairBackoffMax   = 30 * time.Minute
	pairPendingMax   = 16
	pairPendingTTL   = 60 * time.Second
	pairMaxBody      = 16 << 10
	pairTimeout      = 30 * time.Second
)

// ErrPairPassword means a connection password breaks the length or whitespace rules.
var ErrPairPassword = errors.New("连接密码长度须为 10 到 128 个字符，首尾不能有空白")

// PairErrorReason classifies a failed pairing; the control channel maps it to an error code.
type PairErrorReason string

const (
	PairWrongPassword PairErrorReason = "wrong_password"
	PairNoPassword    PairErrorReason = "no_password"
	PairUnsupportedBy PairErrorReason = "unsupported_peer"
	PairUnreachable   PairErrorReason = "unreachable"
	PairBadPeer       PairErrorReason = "bad_peer"
	PairRateLimited   PairErrorReason = "rate_limited"
	// PairFingerprintMismatch is the peer presenting a certificate other than the one the user named.
	// Nothing is confirmed or pinned when it happens.
	PairFingerprintMismatch PairErrorReason = "fingerprint_mismatch"
)

// PairError is a pairing that did not complete. RetryAfter is set for PairRateLimited.
type PairError struct {
	Reason     PairErrorReason
	RetryAfter time.Duration
}

func (e *PairError) Error() string {
	switch e.Reason {
	case PairWrongPassword:
		return "连接密码不正确，或对端不是待配对的 xmesh 设备"
	case PairNoPassword:
		return "对端尚未设置连接密码"
	case PairUnsupportedBy:
		return "对端版本不支持连接密码配对"
	case PairRateLimited:
		return "对端正在限制配对尝试，请稍后再试"
	case PairBadPeer:
		return "对端返回的配对消息无效"
	case PairFingerprintMismatch:
		return "对端证书与指定的指纹不一致"
	default:
		return "无法连接到对端"
	}
}

// PairPasswordState is what the panel shows about this device's connection password. It never
// carries the password or the derived key.
type PairPasswordState struct {
	Set            bool
	UpdatedAt      time.Time
	FailedAttempts int
	LockedUntil    time.Time
}

// DiscoveredPeer is an unpaired device that announced itself on the LAN.
type DiscoveredPeer struct {
	Name        string
	Fingerprint string
	Address     string
	SeenAt      time.Time
}

// pairPasswordRecord is the file lan/pair-password.json (0600). The key is the Argon2id output; the
// password is not stored.
type pairPasswordRecord struct {
	Version   int       `json:"version"`
	Salt      []byte    `json:"salt"`
	Key       string    `json:"key"`
	KDF       string    `json:"kdf"`
	UpdatedAt time.Time `json:"updated_at"`
}

// lanPairing is the pairing state of one LAN node.
type lanPairing struct {
	mu       sync.Mutex
	dir      string
	record   *pairPasswordRecord
	failures int
	until    time.Time
	pending  map[string]pendingExchange
}

type pendingExchange struct {
	responder *spake2.Responder
	certA     *x509.Certificate
	created   time.Time
}

// ValidatePairPassword applies the rules of the connection password.
func ValidatePairPassword(password string) error {
	n := utf8.RuneCountInString(password)
	if n < pairMinRunes || n > pairMaxRunes || strings.TrimSpace(password) != password {
		return ErrPairPassword
	}
	return nil
}

func loadPairRecord(dir string) (*pairPasswordRecord, error) {
	b, err := os.ReadFile(filepath.Join(dir, pairPasswordFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec pairPasswordRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(rec.Key)
	if err != nil || rec.Version != 1 || rec.KDF != pairKDF || len(rec.Salt) != spake2.SaltSize || len(key) != spake2.KeySize {
		return nil, fmt.Errorf("连接密码文件无效")
	}
	return &rec, nil
}

// SetPairPassword stores a new connection password. Pairing with it is possible at once; any lockout
// and pending exchanges are cleared.
func (l *LAN) SetPairPassword(password string) (PairPasswordState, error) {
	if err := ValidatePairPassword(password); err != nil {
		return PairPasswordState{}, err
	}
	salt := make([]byte, spake2.SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return PairPasswordState{}, err
	}
	rec := &pairPasswordRecord{Version: 1, Salt: salt, Key: hex.EncodeToString(spake2.Derive(password, salt)), KDF: pairKDF, UpdatedAt: time.Now().UTC().Truncate(time.Second)}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return PairPasswordState{}, err
	}
	l.pairing.mu.Lock()
	defer l.pairing.mu.Unlock()
	if err := os.MkdirAll(l.pairing.dir, 0700); err != nil {
		return PairPasswordState{}, err
	}
	if err := writePrivate(filepath.Join(l.pairing.dir, pairPasswordFile), b); err != nil {
		return PairPasswordState{}, err
	}
	l.pairing.record = rec
	l.pairing.failures = 0
	l.pairing.until = time.Time{}
	l.pairing.pending = nil
	return l.pairPasswordStateLocked(), nil
}

// ClearPairPassword removes the connection password, which turns off incoming pairing.
func (l *LAN) ClearPairPassword() error {
	l.pairing.mu.Lock()
	defer l.pairing.mu.Unlock()
	if err := os.Remove(filepath.Join(l.pairing.dir, pairPasswordFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l.pairing.record = nil
	l.pairing.failures = 0
	l.pairing.until = time.Time{}
	l.pairing.pending = nil
	return nil
}

// PairPasswordState reports whether a connection password is set and whether incoming pairing is
// locked out.
func (l *LAN) PairPasswordState() PairPasswordState {
	l.pairing.mu.Lock()
	defer l.pairing.mu.Unlock()
	return l.pairPasswordStateLocked()
}

func (l *LAN) pairPasswordStateLocked() PairPasswordState {
	st := PairPasswordState{FailedAttempts: l.pairing.failures}
	if l.pairing.record != nil {
		st.Set = true
		st.UpdatedAt = l.pairing.record.UpdatedAt
	}
	if l.pairing.until.After(time.Now()) {
		st.LockedUntil = l.pairing.until
	}
	return st
}

// begin counts one incoming attempt. It refuses while the node is locked out, and returns how long
// the lockout still has to run.
func (p *lanPairing) begin(now time.Time) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Before(p.until) {
		return p.until.Sub(now), false
	}
	p.failures++
	if p.failures >= pairFreeAttempts {
		p.until = now.Add(pairBackoff(p.failures))
	}
	return 0, true
}

// pairBackoff is the lockout after the given number of attempts: 30 seconds, doubling, capped.
func pairBackoff(failures int) time.Duration {
	shift := failures - pairFreeAttempts
	if shift >= 10 {
		return pairBackoffMax
	}
	return min(pairBackoffBase<<shift, pairBackoffMax)
}

// succeeded clears the count after a completed pairing.
func (p *lanPairing) succeeded() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = 0
	p.until = time.Time{}
}

// park keeps an exchange until its confirmation arrives, and returns the id the initiator sends back.
func (p *lanPairing) park(e pendingExchange) (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = map[string]pendingExchange{}
	}
	now := time.Now()
	for sid, old := range p.pending {
		if now.Sub(old.created) > pairPendingTTL {
			delete(p.pending, sid)
		}
	}
	for len(p.pending) >= pairPendingMax {
		var oldest string
		for sid, old := range p.pending {
			if oldest == "" || old.created.Before(p.pending[oldest].created) {
				oldest = sid
			}
		}
		delete(p.pending, oldest)
	}
	sid := base64.RawURLEncoding.EncodeToString(id)
	p.pending[sid] = e
	return sid, nil
}

// take removes an exchange. Each one can be confirmed once.
func (p *lanPairing) take(sid string) (pendingExchange, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.pending[sid]
	delete(p.pending, sid)
	return e, ok && time.Since(e.created) <= pairPendingTTL
}

func (l *LAN) passwordSecret() (spake2.Secret, bool) {
	l.pairing.mu.Lock()
	rec := l.pairing.record
	l.pairing.mu.Unlock()
	if rec == nil {
		return spake2.Secret{}, false
	}
	key, err := hex.DecodeString(rec.Key)
	if err != nil {
		return spake2.Secret{}, false
	}
	secret, err := spake2.NewSecret(key)
	return secret, err == nil
}

// ServeAgent wraps the agent's handler for the LAN listener. The pairing routes answer any handshake
// that completed, because they are protected by the password exchange and the rate limit. Every other
// route needs a trusted client certificate; a request without one gets 404.
func (l *LAN) ServeAgent(next http.Handler) http.Handler {
	pairing := l.PairingHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, PairPath) {
			pairing.ServeHTTP(w, r)
			return
		}
		if !l.trustedRequest(r) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// trustedRequest reports whether the handshake presented a certificate in the trust store.
func (l *LAN) trustedRequest(r *http.Request) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		return false
	}
	_, err := l.trust.verifyPeer(r.TLS.PeerCertificates[0], "")
	return err == nil
}

// PairingHandler serves the routes under PairPath.
func (l *LAN) PairingHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /lan/pair/challenge", l.serveChallenge)
	mux.HandleFunc("POST /lan/pair/exchange", l.serveExchange)
	mux.HandleFunc("POST /lan/pair/confirm", l.serveConfirm)
	return mux
}

type pairChallengeBody struct {
	Name string `json:"name"`
	Salt []byte `json:"salt"`
	KDF  string `json:"kdf"`
}

func (l *LAN) serveChallenge(w http.ResponseWriter, _ *http.Request) {
	l.pairing.mu.Lock()
	rec := l.pairing.record
	l.pairing.mu.Unlock()
	if rec == nil {
		writePairError(w, http.StatusConflict, "no_password", 0)
		return
	}
	writePairJSON(w, http.StatusOK, pairChallengeBody{Name: l.name, Salt: rec.Salt, KDF: pairKDF})
}

type pairExchangeRequest struct {
	X    []byte `json:"x"`
	Cert string `json:"cert"`
}

type pairExchangeResponse struct {
	SID     string `json:"sid"`
	Y       []byte `json:"y"`
	Cert    string `json:"cert"`
	Confirm []byte `json:"confirm"`
}

func (l *LAN) serveExchange(w http.ResponseWriter, r *http.Request) {
	var req pairExchangeRequest
	if !decodePairBody(w, r, &req) {
		return
	}
	certA, err := parsePeerCertificate(req.Cert)
	if err != nil {
		writePairError(w, http.StatusBadRequest, "bad_request", 0)
		return
	}
	secret, ok := l.passwordSecret()
	if !ok {
		writePairError(w, http.StatusConflict, "no_password", 0)
		return
	}
	if wait, ok := l.pairing.begin(time.Now()); !ok {
		writePairError(w, http.StatusTooManyRequests, "rate_limited", wait)
		return
	}
	responder := spake2.NewResponder(secret, l.cert.Leaf.Raw)
	Y, confirmB, err := responder.Reply(req.X, certA.Raw)
	if err != nil {
		writePairError(w, http.StatusBadRequest, "bad_request", 0)
		return
	}
	sid, err := l.pairing.park(pendingExchange{responder: responder, certA: certA, created: time.Now()})
	if err != nil {
		writePairError(w, http.StatusInternalServerError, "internal", 0)
		return
	}
	writePairJSON(w, http.StatusOK, pairExchangeResponse{SID: sid, Y: Y, Cert: string(l.cert.PEM), Confirm: confirmB})
}

type pairConfirmRequest struct {
	SID     string `json:"sid"`
	Confirm []byte `json:"confirm"`
}

func (l *LAN) serveConfirm(w http.ResponseWriter, r *http.Request) {
	var req pairConfirmRequest
	if !decodePairBody(w, r, &req) {
		return
	}
	pending, ok := l.pairing.take(req.SID)
	if !ok {
		writePairError(w, http.StatusBadRequest, "bad_request", 0)
		return
	}
	if err := pending.responder.Confirm(req.Confirm); err != nil {
		writePairError(w, http.StatusUnprocessableEntity, "wrong_password", 0)
		return
	}
	if err := l.pin(pending.certA); err != nil {
		writePairError(w, http.StatusInternalServerError, "internal", 0)
		return
	}
	l.pairing.succeeded()
	w.WriteHeader(http.StatusNoContent)
}

// pin trusts a certificate that completed the exchange. A certificate already trusted keeps its
// record and its tags.
func (l *LAN) pin(cert *x509.Certificate) error {
	fp := fingerprint(cert)
	if _, ok := l.trust.Find(fp); ok {
		return nil
	}
	peer := Trusted{Name: cert.Subject.CommonName, Fingerprint: fp, CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), Tags: []string{"tag:peer", "tag:device"}}
	if err := l.trust.Add(peer); err != nil {
		return err
	}
	l.notify()
	return nil
}

// PairWith runs the exchange with the node at address (host or host:port) using the password the
// user typed. On success this side pins the peer and returns its record. The peer pins this side
// when the confirmation arrives. A non-empty expected fingerprint must match the peer's certificate
// after the password has been checked and before anything is confirmed or pinned.
func (l *LAN) PairWith(ctx context.Context, address, password, expected string) (Trusted, error) {
	ctx, cancel := context.WithTimeout(ctx, pairTimeout)
	defer cancel()
	base := "https://" + pairAddress(address)
	client := l.pairingClient()
	defer client.CloseIdleConnections()

	var challenge pairChallengeBody
	if err := pairPost(ctx, client, base+"/lan/pair/challenge", struct{}{}, &challenge); err != nil {
		return Trusted{}, err
	}
	if challenge.KDF != pairKDF || len(challenge.Salt) != spake2.SaltSize {
		return Trusted{}, &PairError{Reason: PairBadPeer}
	}
	secret, err := spake2.NewSecret(spake2.Derive(password, challenge.Salt))
	if err != nil {
		return Trusted{}, &PairError{Reason: PairBadPeer}
	}
	initiator, X, err := spake2.NewInitiator(secret, l.cert.Leaf.Raw)
	if err != nil {
		return Trusted{}, &PairError{Reason: PairBadPeer}
	}
	var exchanged pairExchangeResponse
	if err := pairPost(ctx, client, base+"/lan/pair/exchange", pairExchangeRequest{X: X, Cert: string(l.cert.PEM)}, &exchanged); err != nil {
		return Trusted{}, err
	}
	peer, err := parsePeerCertificate(exchanged.Cert)
	if err != nil {
		return Trusted{}, &PairError{Reason: PairBadPeer}
	}
	confirmA, err := initiator.Finish(exchanged.Y, peer.Raw, exchanged.Confirm)
	if errors.Is(err, spake2.ErrPassword) {
		return Trusted{}, &PairError{Reason: PairWrongPassword}
	}
	if err != nil {
		return Trusted{}, &PairError{Reason: PairBadPeer}
	}
	if expected != "" && expected != fingerprint(peer) {
		return Trusted{}, &PairError{Reason: PairFingerprintMismatch}
	}
	if err := pairPost(ctx, client, base+"/lan/pair/confirm", pairConfirmRequest{SID: exchanged.SID, Confirm: confirmA}, nil); err != nil {
		return Trusted{}, err
	}
	if fingerprint(peer) == l.cert.Fingerprint {
		return Trusted{}, &PairError{Reason: PairBadPeer}
	}
	if err := l.pin(peer); err != nil {
		return Trusted{}, err
	}
	trusted, _ := l.trust.Find(fingerprint(peer))
	return trusted, nil
}

// pairingClient dials only handshakes that negotiate PairALPN. The certificate is not checked
// here: SPAKE2 authenticates the peer, and its confirmation covers both certificates.
func (l *LAN) pairingClient() *http.Client {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		NextProtos:         []string{PairALPN},
		Certificates:       []tls.Certificate{l.cert.Certificate},
	}
	// Dialing with DialTLSContext keeps net/http from negotiating HTTP/2 on its own.
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: cfg}
	transport := &http.Transport{DialTLSContext: dialer.DialContext, DisableKeepAlives: true}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// pairAddress makes host:port from what the user typed, with the agent port by default.
func pairAddress(address string) string {
	address = strings.TrimSpace(address)
	if host, port, err := net.SplitHostPort(address); err == nil {
		return net.JoinHostPort(host, port)
	}
	return net.JoinHostPort(strings.Trim(address, "[]"), strconv.Itoa(AgentPort))
}

// pairPost sends one JSON request and decodes the reply into out when out is not nil.
func pairPost(ctx context.Context, client *http.Client, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return &PairError{Reason: PairUnreachable}
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, pairMaxBody))
	if err != nil {
		return &PairError{Reason: PairUnreachable}
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return &PairError{Reason: PairUnsupportedBy}
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error        string `json:"error"`
			RetryAfterMS int64  `json:"retry_after_ms"`
		}
		_ = json.Unmarshal(reply, &e)
		switch e.Error {
		case "no_password":
			return &PairError{Reason: PairNoPassword}
		case "rate_limited":
			return &PairError{Reason: PairRateLimited, RetryAfter: time.Duration(e.RetryAfterMS) * time.Millisecond}
		case "wrong_password":
			return &PairError{Reason: PairWrongPassword}
		}
		return &PairError{Reason: PairBadPeer}
	}
	if out != nil {
		if err := json.Unmarshal(reply, out); err != nil {
			return &PairError{Reason: PairBadPeer}
		}
	}
	return nil
}

// parsePeerCertificate reads one PEM certificate that a peer sent.
func parsePeerCertificate(text string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("证书 PEM 无效")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if cert.Subject.CommonName == "" {
		return nil, errors.New("证书缺少节点名")
	}
	return cert, nil
}

func decodePairBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, pairMaxBody)).Decode(v); err != nil {
		writePairError(w, http.StatusBadRequest, "bad_request", 0)
		return false
	}
	return true
}

func writePairJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("lan_pair_write", "error", err)
	}
}

func writePairError(w http.ResponseWriter, status int, code string, retry time.Duration) {
	body := map[string]any{"error": code}
	if retry > 0 {
		body["retry_after_ms"] = retry.Milliseconds()
	}
	writePairJSON(w, status, body)
}

// discoverUnpaired remembers an announcement from a device that is not paired yet. Only a
// pairing-capable announcement counts, and only until its TTL runs out.
func (l *LAN) discoverUnpaired(fp string, ent *mdns.Entry, txt map[string]string, ttl time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.found == nil {
		l.found = map[string]lanPeer{}
	}
	if ttl <= 0 || txt["pairing"] != "1" {
		delete(l.found, fp)
		return
	}
	address := ""
	for _, ip := range ent.AddrIPv4 {
		if ip.IsPrivate() || ip.IsLoopback() {
			address = net.JoinHostPort(ip.String(), strconv.Itoa(ent.Port))
			break
		}
	}
	if address == "" {
		return
	}
	previous, listed := l.found[fp]
	peer := Peer{ID: "found-" + fp[:24], Name: ent.Instance, Address: address, Fingerprint: fp}
	l.found[fp] = lanPeer{peer: peer, seen: time.Now(), ttl: min(ttl, 30*time.Second)}
	if !listed || previous.peer.Address != address {
		l.notify()
	}
}

// Discovered lists the pairing-capable devices that announced themselves and are not paired.
func (l *LAN) Discovered(context.Context) ([]DiscoveredPeer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []DiscoveredPeer{}
	for fp, f := range l.found {
		if time.Since(f.seen) > f.ttl {
			delete(l.found, fp)
			continue
		}
		if _, paired := l.trust.Find(fp); paired {
			continue
		}
		out = append(out, DiscoveredPeer{Name: f.peer.Name, Fingerprint: fp, Address: f.peer.Address, SeenAt: f.seen})
	}
	return out, nil
}
