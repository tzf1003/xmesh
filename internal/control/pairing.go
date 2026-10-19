package control

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/mesh"
)

// PairingAdmin is the LAN pairing as the control channel sees it. Only the LAN network has one.
type PairingAdmin interface {
	PairPasswordState() mesh.PairPasswordState
	SetPairPassword(password string) (mesh.PairPasswordState, error)
	ClearPairPassword() error
	Discovered(ctx context.Context) ([]mesh.DiscoveredPeer, error)
	// PairWith pairs with the device at address using its connection password. A non-empty expected
	// fingerprint must match the device's certificate.
	PairWith(ctx context.Context, address, password, expected string) (mesh.Trusted, error)
}

var pairFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) pairingAdmin(w http.ResponseWriter) PairingAdmin {
	if !s.requireLAN(w) {
		return nil
	}
	if s.o.Pairing == nil {
		failWith(w, newError(v1.ErrUnavailable, "配对尚未就绪"))
		return nil
	}
	return s.o.Pairing
}

// Only nodes serving an Agent listener accept incoming password pairing.
// Outgoing discovery and connection remain available to LAN gateways.
func (s *Server) pairingListenerAdmin(w http.ResponseWriter) PairingAdmin {
	admin := s.pairingAdmin(w)
	if admin == nil {
		return nil
	}
	if !s.o.Store.Get().HasRole("agent") {
		failWith(w, newError(v1.ErrRoleDisabled, "连接密码需要 Agent 角色"))
		return nil
	}
	return admin
}

// pairPasswordView turns the mesh state into the contract shape. Unset times are null, not zero.
func pairPasswordView(st mesh.PairPasswordState) v1.PairPasswordState {
	out := v1.PairPasswordState{Set: st.Set, FailedAttempts: st.FailedAttempts}
	if !st.UpdatedAt.IsZero() {
		t := v1.At(st.UpdatedAt)
		out.UpdatedAt = &t
	}
	if !st.LockedUntil.IsZero() {
		t := v1.At(st.LockedUntil)
		out.LockedUntil = &t
	}
	return out
}

func (s *Server) getPairPassword(w http.ResponseWriter, _ *http.Request, _ *call) {
	admin := s.pairingListenerAdmin(w)
	if admin == nil {
		return
	}
	writeJSON(w, 200, pairPasswordView(admin.PairPasswordState()))
}

func (s *Server) putPairPassword(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.pairingListenerAdmin(w)
	if admin == nil {
		return
	}
	var req v1.PairPasswordRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	st, err := admin.SetPairPassword(req.Password)
	switch {
	case errors.Is(err, mesh.ErrPairPassword):
		failWith(w, argError("password", mesh.ErrPairPassword.Error()))
	case err != nil:
		failWith(w, newError(v1.ErrInternal, "无法保存连接密码"))
	default:
		writeJSON(w, 200, pairPasswordView(st))
	}
}

func (s *Server) deletePairPassword(w http.ResponseWriter, _ *http.Request, _ *call) {
	admin := s.pairingListenerAdmin(w)
	if admin == nil {
		return
	}
	if err := admin.ClearPairPassword(); err != nil {
		failWith(w, newError(v1.ErrInternal, "无法清除连接密码"))
		return
	}
	w.WriteHeader(204)
}

func (s *Server) listPairDiscovered(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.pairingAdmin(w)
	if admin == nil {
		return
	}
	found, err := admin.Discovered(r.Context())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取内网发现结果"))
		return
	}
	out := v1.PairDiscoveredResponse{Devices: []v1.PairDiscoveredDevice{}}
	for _, d := range found {
		out.Devices = append(out.Devices, v1.PairDiscoveredDevice{Name: d.Name, Fingerprint: d.Fingerprint, Address: d.Address, SeenAt: v1.At(d.SeenAt)})
	}
	writeJSON(w, 200, out)
}

func (s *Server) connectPair(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.pairingAdmin(w)
	if admin == nil {
		return
	}
	var req v1.PairConnectRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if err := mesh.ValidatePairPassword(req.Password); err != nil {
		failWith(w, argError("password", mesh.ErrPairPassword.Error()))
		return
	}
	if req.Fingerprint != "" && !pairFingerprintPattern.MatchString(req.Fingerprint) {
		failWith(w, argError("fingerprint", "指纹必须是 64 位小写十六进制"))
		return
	}
	address := strings.TrimSpace(req.Address)
	if address == "" && req.Fingerprint == "" {
		failWith(w, argError("address", "请填写对端地址，或选择内网中发现的设备"))
		return
	}
	if address == "" {
		found, err := admin.Discovered(r.Context())
		if err != nil {
			failWith(w, newError(v1.ErrInternal, "无法读取内网发现结果"))
			return
		}
		for _, d := range found {
			if d.Fingerprint == req.Fingerprint {
				address = d.Address
				break
			}
		}
		if address == "" {
			failWith(w, newError(v1.ErrNotFound, "内网中没有发现这台设备"))
			return
		}
	}
	trusted, err := admin.PairWith(r.Context(), address, req.Password, req.Fingerprint)
	if err != nil {
		failWith(w, pairError(err))
		return
	}
	writeJSON(w, 200, v1.PairConnectResponse{Name: trusted.Name, Fingerprint: trusted.Fingerprint, Address: address, Tags: trusted.Tags})
}

// pairError maps a failed exchange to the contract. A wrong password and a man in the middle look
// the same from here, so both are pair_rejected with reason wrong_password.
func pairError(err error) *v1.Error {
	var pe *mesh.PairError
	if !errors.As(err, &pe) {
		return newError(v1.ErrPairRejected, "无法连接到对端")
	}
	switch pe.Reason {
	case mesh.PairRateLimited:
		e := newError(v1.ErrRateLimited, pe.Error())
		ms := pe.RetryAfter.Milliseconds()
		e.RetryAfterMS = &ms
		return e
	case mesh.PairFingerprintMismatch:
		e := argError("fingerprint", pe.Error())
		e.Reason = "fingerprint_mismatch"
		return e
	default:
		e := newError(v1.ErrPairRejected, pe.Error())
		e.Reason = string(pe.Reason)
		return e
	}
}
