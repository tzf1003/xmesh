package control

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
)

// OfferAdmin is the node's connection-method inbox and its way to deliver an offer to a paired device.
type OfferAdmin interface {
	SendOffer(ctx context.Context, deviceID string, offer device.Offer) error
	Offers(ctx context.Context) ([]device.ReceivedOffer, error)
	TakeOffer(ctx context.Context, id string) (device.ReceivedOffer, error)
	PutOffer(ctx context.Context, offer device.ReceivedOffer) error
}

func (s *Server) offerAdmin(w http.ResponseWriter) OfferAdmin {
	if s.o.Offers == nil {
		failWith(w, newError(v2.ErrUnavailable, "设备连接服务尚未就绪"))
		return nil
	}
	return s.o.Offers
}

func offerView(o device.ReceivedOffer) v2.OfferSummary {
	out := v2.OfferSummary{ID: o.ID, Kind: o.Kind, FromID: o.FromID, FromName: o.FromName, ReceivedAt: v2.At(o.ReceivedAt), ExpiresAt: v2.At(o.ExpiresAt)}
	if o.Cloudflare != nil {
		out.Cloudflare = &v2.OfferCloudflareSummary{Hostname: o.Cloudflare.Hostname, PeerHostname: o.Cloudflare.PeerHostname}
	}
	return out
}

func (s *Server) sendOffer(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.offerAdmin(w)
	if admin == nil {
		return
	}
	var req v2.OfferSendRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if !strings.HasPrefix(req.DeviceID, "dev-") || !pairFingerprintPattern.MatchString(strings.TrimPrefix(req.DeviceID, "dev-")) {
		failWith(w, argError("device_id", "设备 ID 无效"))
		return
	}
	offer := device.Offer{ID: req.Offer.ID, Kind: req.Offer.Kind}
	if c := req.Offer.Cloudflare; c != nil {
		offer.Cloudflare = &device.CloudflareOffer{Hostname: c.Hostname, TunnelToken: c.TunnelToken, AccessID: c.AccessID, AccessSecret: c.AccessSecret, PeerHostname: c.PeerHostname}
	}
	if err := offer.Validate(); err != nil {
		failWith(w, argError("offer", err.Error()))
		return
	}
	switch err := admin.SendOffer(r.Context(), req.DeviceID, offer); {
	case errors.Is(err, device.ErrUntrusted):
		failWith(w, newError(v2.ErrNotFound, "该设备尚未配对"))
	case err != nil:
		failWith(w, newError(v2.ErrUnavailable, "无法把连接方式送达该设备："+oneLine(err.Error())))
	default:
		w.WriteHeader(204)
	}
}

func (s *Server) listOffers(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.offerAdmin(w)
	if admin == nil {
		return
	}
	offers, err := admin.Offers(r.Context())
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "无法读取收到的连接方式"))
		return
	}
	out := v2.OffersResponse{Offers: []v2.OfferSummary{}}
	for _, o := range offers {
		out.Offers = append(out.Offers, offerView(o))
	}
	writeJSON(w, 200, out)
}

func offerIDArg(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := pathPart(r, 2)
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		failWith(w, argError("offer_id", "连接方式编号无效"))
		return "", false
	}
	return id, true
}

func (s *Server) rejectOffer(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.offerAdmin(w)
	if admin == nil {
		return
	}
	id, ok := offerIDArg(w, r)
	if !ok {
		return
	}
	switch _, err := admin.TakeOffer(r.Context(), id); {
	case errors.Is(err, device.ErrOfferUnknown):
		failWith(w, newError(v2.ErrNotFound, "没有这条连接方式提议"))
	case err != nil:
		failWith(w, newError(v2.ErrInternal, "无法删除连接方式提议"))
	default:
		w.WriteHeader(204)
	}
}

// defaultTunnelListen is where the tunnel an offer provisions forwards to, so a device that listens
// elsewhere cannot use the offer.
const defaultTunnelListen = "127.0.0.1:7434"

// acceptOffer applies a received offer: it writes the device's own tunnel and Access credentials to
// private files and records them, and the sender's hostname as that device's address, in
// mesh.cloudflare. Either all of it happens or the offer goes back to the inbox untouched.
func (s *Server) acceptOffer(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.offerAdmin(w)
	if admin == nil {
		return
	}
	id, ok := offerIDArg(w, r)
	if !ok {
		return
	}
	offer, err := admin.TakeOffer(r.Context(), id)
	switch {
	case errors.Is(err, device.ErrOfferUnknown):
		failWith(w, newError(v2.ErrNotFound, "没有这条连接方式提议"))
		return
	case err != nil:
		failWith(w, newError(v2.ErrInternal, "无法读取连接方式提议"))
		return
	}
	applied := false
	defer func() {
		if !applied {
			_ = admin.PutOffer(context.WithoutCancel(r.Context()), offer)
		}
	}()
	if offer.Kind != device.OfferCloudflare || offer.Cloudflare == nil {
		failWith(w, newError(v2.ErrInvalidArgs, "不支持这种连接方式"))
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "无法读取配置文件"))
		return
	}
	mesh, _ := parsedOf(old)["mesh"].(map[string]any)
	current, _ := mesh["cloudflare"].(map[string]any)
	for _, key := range []string{"hostname", "access_client_id", "access_client_secret_file", "tunnel_token_file"} {
		if v, _ := current[key].(string); v != "" {
			failWith(w, newError(v2.ErrConflict, "本设备已经配置了 Cloudflare，请先移除后再接受"))
			return
		}
	}
	if listen, _ := current["listen"].(string); listen != "" && listen != defaultTunnelListen {
		failWith(w, newError(v2.ErrConflict, "本设备的 Cloudflare 监听地址不是默认值 "+defaultTunnelListen+"，下发的隧道无法转发过来"))
		return
	}
	c := offer.Cloudflare
	dir := filepath.Join(s.o.StateDir, "cloudflare")
	tunnelFile, secretFile := filepath.Join(dir, "tunnel-"+id[:8]), filepath.Join(dir, "access-"+id[:8])
	written := []string{}
	cleanup := func() {
		for _, f := range written {
			_ = os.Remove(f)
		}
	}
	for _, f := range []struct{ path, value string }{{tunnelFile, c.TunnelToken}, {secretFile, c.AccessSecret}} {
		if err := createConfigAuthkey(f.path, f.value); err != nil {
			cleanup()
			failWith(w, newError(v2.ErrInternal, "无法保存 Cloudflare 凭据"))
			return
		}
		written = append(written, f.path)
	}
	edits := []Edit{
		{Path: "/mesh/cloudflare/enabled", Value: true},
		{Path: "/mesh/cloudflare/hostname", Value: c.Hostname},
		{Path: "/mesh/cloudflare/tunnel_token_file", Value: tunnelFile},
		{Path: "/mesh/cloudflare/access_client_id", Value: c.AccessID},
		{Path: "/mesh/cloudflare/access_client_secret_file", Value: secretFile},
		{Path: "/mesh/cloudflare/peers/" + offer.FromID, Value: c.PeerHostname},
	}
	next, err := ApplyEdits(old, edits)
	if err != nil {
		cleanup()
		failWith(w, newError(v2.ErrConflict, "配置结构不允许这项修改"))
		return
	}
	if _, err := config.Parse(next); err != nil {
		cleanup()
		e := newError(v2.ErrConfigInvalid, "配置未通过校验，没有写入")
		e.Diagnostics = diagnostics(err)
		failWith(w, e)
		return
	}
	diff := diffConfigs(parsedOf(old), parsedOf(next))
	_, restart, err := s.storeConfigLocked(old, next, diff)
	if err != nil {
		cleanup()
		if errors.Is(err, errConfigChanged) {
			failWith(w, newError(v2.ErrConflict, "配置已变化，请重试"))
			return
		}
		failWith(w, newError(v2.ErrInternal, "写入配置文件失败"))
		return
	}
	applied = true
	paths := make([]string, len(diff))
	for i, change := range diff {
		paths[i] = change.Pointer
	}
	slices.Sort(paths)
	writeJSON(w, 200, v2.OfferAcceptResponse{Kind: offer.Kind, RestartRequired: restart, ChangedPaths: paths})
}
