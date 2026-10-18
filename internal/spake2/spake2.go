// Package spake2 is the password exchange behind LAN pairing with a connection password
// (docs/design/14-password-pairing.md). The message flow follows SPAKE2 (RFC 9382) over the
// ristretto255 group from circl. The fixed points M and N are hashed from domain-separated labels,
// so their discrete logarithms are unknown.
//
// The exchange authenticates the password, not the certificates. Both certificates are part of the
// transcript, so each side confirms the exact identity the other side pins.
package spake2

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/cloudflare/circl/group"
	"golang.org/x/crypto/argon2"
)

var grp = group.Ristretto255

const (
	// KeySize is the length of the password key that Derive returns and that a Secret is made from.
	KeySize = 64
	// SaltSize is the length of the random salt chosen by the side that stores the password.
	SaltSize = 16

	transcriptLabel = "xmesh-pair/v1"
	pointM          = "xmesh-pair/v1/spake2 M"
	pointN          = "xmesh-pair/v1/spake2 N"
	scalarW0        = "xmesh-pair/v1/w0"
	scalarW1        = "xmesh-pair/v1/w1"
)

// Argon2id parameters of the kdf "argon2id-v1" (RFC 9106, second recommended option).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
)

var (
	// ErrPassword means the two sides do not share the password, or the peer is not one.
	ErrPassword = errors.New("spake2: 口令不一致或对端不是待配对设备")
	// ErrMessage means a message is malformed: a point that does not decode, the identity, or a
	// wrong length.
	ErrMessage = errors.New("spake2: 消息格式无效")

	mPoint = grp.HashToElement(nil, []byte(pointM))
	nPoint = grp.HashToElement(nil, []byte(pointN))
)

// Derive stretches the password with Argon2id. The stored form of a password is this key plus the
// salt; the password itself is never kept.
func Derive(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, KeySize)
}

// Secret is the pair of scalars one password yields.
type Secret struct {
	w0, w1 group.Scalar
}

// NewSecret makes the scalars from a key returned by Derive.
func NewSecret(key []byte) (Secret, error) {
	if len(key) != KeySize {
		return Secret{}, ErrMessage
	}
	return Secret{
		w0: grp.HashToScalar(key[:KeySize/2], []byte(scalarW0)),
		w1: grp.HashToScalar(key[KeySize/2:], []byte(scalarW1)),
	}, nil
}

// Initiator is the side that typed the password.
type Initiator struct {
	secret Secret
	x      group.Scalar
	x0     group.Element
	certA  []byte
}

// NewInitiator starts the exchange and returns the first message, X. certA is the DER certificate
// the initiator presents.
func NewInitiator(secret Secret, certA []byte) (*Initiator, []byte, error) {
	x := grp.RandomNonZeroScalar(rand.Reader)
	// X = x·G + w0·M
	blind := grp.NewElement().Mul(mPoint, secret.w0)
	X := grp.NewElement().MulGen(x)
	X.Add(X, blind)
	xb, err := encodePoint(X)
	if err != nil {
		return nil, nil, err
	}
	return &Initiator{secret: secret, x: x, x0: X, certA: certA}, xb, nil
}

// Finish takes the responder's Y, its certificate and its confirmation code. It returns the
// initiator's confirmation code, or ErrPassword when the responder cannot prove the same password.
func (in *Initiator) Finish(Y, certB, confirmB []byte) ([]byte, error) {
	y, err := decodePoint(Y)
	if err != nil {
		return nil, err
	}
	// Z = x·(Y − w1·N)
	unblind := grp.NewElement().Mul(nPoint, in.secret.w1)
	unblind.Neg(unblind)
	shared := grp.NewElement().Add(y, unblind)
	shared.Mul(shared, in.x)
	tt, err := transcript(in.secret, in.x0, y, shared, in.certA, certB)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(tt)
	if !hmac.Equal(confirmB, mac(key[:], "B", tt)) {
		return nil, ErrPassword
	}
	return mac(key[:], "A", tt), nil
}

// Responder is the side that holds the password.
type Responder struct {
	secret  Secret
	certB   []byte
	tt      []byte
	confirm []byte
}

// NewResponder prepares a responder for one exchange. certB is the DER certificate the responder
// presents and pins.
func NewResponder(secret Secret, certB []byte) *Responder {
	return &Responder{secret: secret, certB: certB}
}

// Reply takes the initiator's X and certificate. It returns Y and the responder's confirmation
// code. The responder keeps what it needs to check the initiator's confirmation in Confirm.
func (r *Responder) Reply(X, certA []byte) (Y, confirmB []byte, err error) {
	x, err := decodePoint(X)
	if err != nil {
		return nil, nil, err
	}
	y := grp.RandomNonZeroScalar(rand.Reader)
	// Y = y·G + w1·N
	blind := grp.NewElement().Mul(nPoint, r.secret.w1)
	Yp := grp.NewElement().MulGen(y)
	Yp.Add(Yp, blind)
	// Z = y·(X − w0·M)
	unblind := grp.NewElement().Mul(mPoint, r.secret.w0)
	unblind.Neg(unblind)
	shared := grp.NewElement().Add(x, unblind)
	shared.Mul(shared, y)
	tt, err := transcript(r.secret, x, Yp, shared, certA, r.certB)
	if err != nil {
		return nil, nil, err
	}
	yb, err := encodePoint(Yp)
	if err != nil {
		return nil, nil, err
	}
	key := sha256.Sum256(tt)
	r.tt = tt
	r.confirm = mac(key[:], "A", tt)
	return yb, mac(key[:], "B", tt), nil
}

// Confirm checks the initiator's confirmation code against the exchange of Reply.
func (r *Responder) Confirm(confirmA []byte) error {
	if r.tt == nil {
		return ErrMessage
	}
	if !hmac.Equal(confirmA, r.confirm) {
		return ErrPassword
	}
	return nil
}

func decodePoint(b []byte) (group.Element, error) {
	p := grp.NewElement()
	if err := p.UnmarshalBinary(b); err != nil || p.IsIdentity() {
		return nil, ErrMessage
	}
	return p, nil
}

// transcript is the byte string both sides hash. Each part is prefixed with its length, so no two
// different sequences of parts give the same bytes.
func transcript(s Secret, X, Y, Z group.Element, certA, certB []byte) ([]byte, error) {
	w0, err := s.w0.MarshalBinary()
	if err != nil {
		return nil, err
	}
	w1, err := s.w1.MarshalBinary()
	if err != nil {
		return nil, err
	}
	points := make([][]byte, 0, 5)
	for _, p := range []group.Element{mPoint, nPoint, X, Y, Z} {
		b, err := encodePoint(p)
		if err != nil {
			return nil, err
		}
		points = append(points, b)
	}
	parts := [][]byte{[]byte(transcriptLabel), w0, w1, points[0], points[1], points[2], points[3], points[4], certA, certB}
	var out []byte
	for _, part := range parts {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		out = append(out, n[:]...)
		out = append(out, part...)
	}
	return out, nil
}

func encodePoint(p group.Element) ([]byte, error) {
	return p.MarshalBinaryCompress()
}

// mac is HMAC-SHA-256 over the role label and the transcript, keyed by the hash of the transcript.
func mac(key []byte, role string, tt []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(role))
	h.Write(tt)
	return h.Sum(nil)
}
