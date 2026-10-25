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
	// SendOffer returns "delivered" when the device has the offer, or "queued" when it can only
	// collect it over its own connection to this node.
	SendOffer(ctx context.Context, deviceID string, offer device.Offer) (string, error)
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
	if o.Tailscale != nil {
		out.Tailscale = &v2.OfferTailscaleSummary{ControlURL: o.Tailscale.ControlURL, Ephemeral: o.Tailscale.Ephemeral}
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
	if t := req.Offer.Tailscale; t != nil {
		offer.Tailscale = &device.TailscaleOffer{ControlURL: t.ControlURL, AuthKey: t.AuthKey, Ephemeral: t.Ephemeral}
	}
	if err := offer.Validate(); err != nil {
		failWith(w, argError("offer", err.Error()))
		return
	}
	delivery, err := admin.SendOffer(r.Context(), req.DeviceID, offer)
	switch {
	case errors.Is(err, device.ErrUntrusted):
		failWith(w, newError(v2.ErrNotFound, "该设备尚未配对"))
	case err != nil:
		failWith(w, newError(v2.ErrUnavailable, "无法把连接方式送达该设备："+oneLine(err.Error())))
	default:
		writeJSON(w, 200, v2.OfferSendResponse{Delivery: delivery})
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
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v2.ErrInternal, "无法读取配置文件"))
		return
	}
	var edits []Edit
	var written []string
	var apiErr *v2.Error
	switch {
	case offer.Kind == device.OfferCloudflare && offer.Cloudflare != nil:
		edits, written, apiErr = s.cloudflareOfferEdits(old, offer)
	case offer.Kind == device.OfferTailscale && offer.Tailscale != nil:
		edits, written, apiErr = s.tailscaleOfferEdits(old, offer)
	default:
		failWith(w, newError(v2.ErrInvalidArgs, "不支持这种连接方式"))
		return
	}
	cleanup := func() {
		for _, f := range written {
			_ = os.Remove(f)
		}
	}
	if apiErr != nil {
		cleanup()
		failWith(w, apiErr)
		return
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

// cloudflareOfferEdits writes the device's own tunnel and Access credentials to private files and
// returns the configuration edits that use them. written lists the files to remove if the edits fail.
func (s *Server) cloudflareOfferEdits(old []byte, offer device.ReceivedOffer) (edits []Edit, written []string, e *v2.Error) {
	mesh, _ := parsedOf(old)["mesh"].(map[string]any)
	current, _ := mesh["cloudflare"].(map[string]any)
	for _, key := range []string{"hostname", "access_client_id", "access_client_secret_file", "tunnel_token_file"} {
		if v, _ := current[key].(string); v != "" {
			return nil, nil, newError(v2.ErrConflict, "本设备已经配置了 Cloudflare，请先移除后再接受")
		}
	}
	if listen, _ := current["listen"].(string); listen != "" && listen != defaultTunnelListen {
		return nil, nil, newError(v2.ErrConflict, "本设备的 Cloudflare 监听地址不是默认值 "+defaultTunnelListen+"，下发的隧道无法转发过来")
	}
	c := offer.Cloudflare
	dir := filepath.Join(s.o.StateDir, "cloudflare")
	tunnelFile, secretFile := filepath.Join(dir, "tunnel-"+offer.ID[:8]), filepath.Join(dir, "access-"+offer.ID[:8])
	for _, f := range []struct{ path, value string }{{tunnelFile, c.TunnelToken}, {secretFile, c.AccessSecret}} {
		if err := createConfigAuthkey(f.path, f.value); err != nil {
			return nil, written, newError(v2.ErrInternal, "无法保存 Cloudflare 凭据")
		}
		written = append(written, f.path)
	}
	return []Edit{
		{Path: "/mesh/cloudflare/enabled", Value: true},
		{Path: "/mesh/cloudflare/hostname", Value: c.Hostname},
		{Path: "/mesh/cloudflare/tunnel_token_file", Value: tunnelFile},
		{Path: "/mesh/cloudflare/access_client_id", Value: c.AccessID},
		{Path: "/mesh/cloudflare/access_client_secret_file", Value: secretFile},
		{Path: "/mesh/cloudflare/peers/" + offer.FromID, Value: c.PeerHostname},
	}, written, nil
}

// tailscaleOfferEdits stores the pre-authorised key where tailnet auth keys live and returns the edits
// that enable the tailnet with it. A device that already has a tailnet configured keeps it.
func (s *Server) tailscaleOfferEdits(old []byte, offer device.ReceivedOffer) (edits []Edit, written []string, e *v2.Error) {
	mesh, _ := parsedOf(old)["mesh"].(map[string]any)
	current, _ := mesh["tailnet"].(map[string]any)
	if on, _ := current["enabled"].(bool); on {
		return nil, nil, newError(v2.ErrConflict, "本设备已经启用了 Tailnet，请先关闭后再接受")
	}
	for _, key := range []string{"authkey_file", "control_url"} {
		if v, _ := current[key].(string); v != "" {
			return nil, nil, newError(v2.ErrConflict, "本设备已经配置了 Tailnet，请先移除后再接受")
		}
	}
	t := offer.Tailscale
	// authkey-<uuid> is the name configAuthkeyPath accepts, so the panel can later replace this key.
	id := offer.ID
	file := filepath.Join(s.o.StateDir, "authkeys", "authkey-"+id[:8]+"-"+id[8:12]+"-"+id[12:16]+"-"+id[16:20]+"-"+id[20:32])
	if err := createConfigAuthkey(file, t.AuthKey); err != nil {
		return nil, nil, newError(v2.ErrInternal, "无法保存 Tailnet auth key")
	}
	controlURL := t.ControlURL
	if controlURL == "" {
		controlURL = config.DefaultControlURL
	}
	edits = []Edit{
		{Path: "/mesh/tailnet/enabled", Value: true},
		{Path: "/mesh/tailnet/control_url", Value: controlURL},
		// A key replaces browser sign-in, which the schema refuses to combine with authkey_file.
		{Path: "/mesh/tailnet/auth_method", Value: "authkey"},
		{Path: "/mesh/tailnet/authkey_file", Value: file},
	}
	if t.Ephemeral {
		edits = append(edits, Edit{Path: "/mesh/tailnet/ephemeral", Value: true})
	}
	return edits, []string{file}, nil
}
