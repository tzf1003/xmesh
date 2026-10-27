package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/device"
)

// PairingAdmin is the runtime's single device pairing service for all enabled network paths.
type PairingAdmin interface {
	PairPasswordState() device.PairStatus
	SetPairPassword(string) (device.PairStatus, error)
	ClearPairPassword() error
	Discovered(context.Context) ([]v2.PairDiscoveredDevice, error)
	PairWith(context.Context, device.PathKind, string, string, string) (device.Trust, error)
}

var pairFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) pairingAdmin(w http.ResponseWriter) PairingAdmin {
	if s.o.Pairing == nil {
		failWith(w, newError(v2.ErrUnavailable, "设备配对服务尚未就绪"))
		return nil
	}
	return s.o.Pairing
}

func pairPasswordView(st device.PairStatus) v2.PairPasswordState {
	out := v2.PairPasswordState{Set: st.Set, FailedAttempts: st.FailedAttempts}
	if !st.UpdatedAt.IsZero() {
		t := v2.At(st.UpdatedAt)
		out.UpdatedAt = &t
	}
	if !st.LockedUntil.IsZero() {
		t := v2.At(st.LockedUntil)
		out.LockedUntil = &t
	}
	return out
}
func (s *Server) getPairPassword(w http.ResponseWriter, _ *http.Request, _ *call) {
	admin := s.pairingAdmin(w)
	if admin == nil {
		return
	}
	writeJSON(w, 200, pairPasswordView(admin.PairPasswordState()))
}
func (s *Server) putPairPassword(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.pairingAdmin(w)
	if admin == nil {
		return
	}
	var req v2.PairPasswordRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	st, err := admin.SetPairPassword(req.Password)
	switch {
	case errors.Is(err, device.ErrPairPassword):
		failWith(w, argError("password", device.ErrPairPassword.Error()))
	case err != nil:
		failWith(w, newError(v2.ErrInternal, "无法保存连接密码"))
	default:
		writeJSON(w, 200, pairPasswordView(st))
	}
}
func (s *Server) deletePairPassword(w http.ResponseWriter, _ *http.Request, _ *call) {
	admin := s.pairingAdmin(w)
	if admin == nil {
		return
	}
	if err := admin.ClearPairPassword(); err != nil {
		failWith(w, newError(v2.ErrInternal, "无法清除连接密码"))
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
		failWith(w, newError(v2.ErrInternal, "无法读取设备发现结果"))
		return
	}
	if found == nil {
		found = []v2.PairDiscoveredDevice{}
	}
	writeJSON(w, 200, v2.PairDiscoveredResponse{Devices: found})
}

// pairDefaultPort is the port a bare host is paired on: each path listens on its own.
func pairDefaultPort(kind device.PathKind) string {
	if kind == device.Direct {
		return "7435"
	}
	return "7433"
}

func pairEndpoint(address string, kind device.PathKind) (string, error) {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		if _, _, err := net.SplitHostPort(address); err != nil {
			address = net.JoinHostPort(strings.Trim(address, "[]"), pairDefaultPort(kind))
		}
		address = "https://" + address
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("设备地址需要主机:端口或 HTTPS 端点")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (s *Server) connectPair(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.pairingAdmin(w)
	if admin == nil {
		return
	}
	var req v2.PairConnectRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if err := device.ValidatePassword(req.Password); err != nil {
		failWith(w, argError("password", err.Error()))
		return
	}
	if req.Fingerprint != "" && !pairFingerprintPattern.MatchString(req.Fingerprint) {
		failWith(w, argError("fingerprint", "设备指纹须为 64 位小写十六进制"))
		return
	}
	kind := device.PathKind(req.Path)
	if kind == "" {
		kind = device.LAN
	}
	if !kind.Valid() {
		failWith(w, argError("path", "连接路径须为 lan、tailnet、direct 或 cloudflare"))
		return
	}
	address := strings.TrimSpace(req.Address)
	if address == "" {
		if req.Fingerprint == "" {
			failWith(w, argError("address", "请选择设备或填写连接地址"))
			return
		}
		found, err := admin.Discovered(r.Context())
		if err != nil {
			failWith(w, newError(v2.ErrInternal, "无法读取设备发现结果"))
			return
		}
		for _, candidate := range found {
			if candidate.Fingerprint == req.Fingerprint && candidate.Path == string(kind) {
				address = candidate.Address
				break
			}
		}
		if address == "" {
			failWith(w, newError(v2.ErrNotFound, "所选路径未发现该设备"))
			return
		}
	}
	endpoint, err := pairEndpoint(address, kind)
	if err != nil {
		failWith(w, argError("address", err.Error()))
		return
	}
	expected := ""
	if req.Fingerprint != "" {
		expected = "dev-" + req.Fingerprint
	}
	trusted, err := admin.PairWith(r.Context(), kind, endpoint, req.Password, expected)
	if err != nil {
		failWith(w, pairError(err))
		return
	}
	writeJSON(w, 200, v2.PairConnectResponse{Name: trusted.Document.Name, DeviceID: trusted.Document.DeviceID, Fingerprint: strings.TrimPrefix(trusted.Document.DeviceID, "dev-"), Address: endpoint, Path: string(kind), Tags: trusted.Tags})
}

func pairError(err error) *v2.Error {
	code, reason := v2.ErrPairRejected, "unreachable"
	switch {
	case errors.Is(err, device.ErrIdentityMismatch):
		out := argError("fingerprint", err.Error())
		out.Reason = "fingerprint_mismatch"
		return out
	case errors.Is(err, device.ErrPairUnsupported):
		reason = "unsupported_peer"
	case errors.Is(err, device.ErrPairPassword):
		return argError("password", err.Error())
	case errors.Is(err, device.ErrPairProof):
		reason = "wrong_password"
	case errors.Is(err, device.ErrPairDisabled):
		reason = "no_password"
	case errors.Is(err, device.ErrGeneration):
		code, reason = v2.ErrConflict, "generation_changed"
	case errors.Is(err, device.ErrPairLimited):
		out := newError(v2.ErrRateLimited, "配对尝试过多，请稍后重试")
		ms := int64(30000)
		out.RetryAfterMS = &ms
		return out
	}
	out := newError(code, "设备配对未完成")
	out.Reason = reason
	return out
}
