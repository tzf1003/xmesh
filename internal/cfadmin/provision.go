package cfadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ServiceTokenLifetime is how long the mesh service token stays valid. Rotating it is a re-run of
// setup; the devices' inner mTLS does not depend on it.
const ServiceTokenLifetime = "8760h"

// TunnelService is where cloudflared forwards a device's tunnel by default.
const TunnelService = "tcp://127.0.0.1:7434"

var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// Device is everything created for one device. Teardown removes it in reverse order.
type Device struct {
	Hostname    string `json:"hostname"`
	TunnelID    string `json:"tunnel_id"`
	TunnelToken string `json:"-"`
	DNSID       string `json:"dns_id"`
	AppID       string `json:"app_id"`
	PolicyID    string `json:"policy_id"`
}

func (d Device) String() string {
	return "Device{hostname=" + d.Hostname + " tunnel=" + d.TunnelID + " token=<redacted>}"
}
func (d Device) GoString() string { return d.String() }

// Mesh creates resources under one domain. Names start with Prefix so a test or a cleanup can find them.
type Mesh struct {
	Client *Client
	Domain string
	Prefix string
	// Service is the loopback TCP service cloudflared forwards to. Defaults to TunnelService.
	Service string
}

func (m *Mesh) prefix() string {
	if m.Prefix == "" {
		return "xmesh"
	}
	return m.Prefix
}

// Label turns a device name into a DNS label.
func Label(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), "-")
	if len(label) > 24 {
		label = strings.TrimRight(label[:24], "-")
	}
	if label == "" {
		label = "device"
	}
	return label
}

// NewServiceToken creates the token every device in the mesh presents to the others.
func (m *Mesh) NewServiceToken(ctx context.Context, name string) (ServiceToken, error) {
	return m.Client.CreateServiceToken(ctx, m.prefix()+"-"+Label(name), ServiceTokenLifetime)
}

// ProvisionDevice creates a tunnel, routes the device's hostname to it, and admits tokenID. A failure
// removes whatever was already created.
func (m *Mesh) ProvisionDevice(ctx context.Context, name, tokenID string) (d Device, err error) {
	if !domainPattern.MatchString(m.Domain) {
		return Device{}, errors.New("Cloudflare 域名无效")
	}
	if tokenID == "" {
		return Device{}, errors.New("缺少 Access 服务令牌")
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return Device{}, err
	}
	resource := fmt.Sprintf("%s-%s-%s", m.prefix(), Label(name), hex.EncodeToString(suffix))
	d.Hostname = resource + "." + m.Domain
	defer func() {
		if err != nil {
			_ = m.Teardown(context.WithoutCancel(ctx), d)
			d = Device{}
		}
	}()
	tunnel, err := m.Client.CreateTunnel(ctx, resource)
	if err != nil {
		return d, err
	}
	d.TunnelID, d.TunnelToken = tunnel.ID, tunnel.Token
	service := m.Service
	if service == "" {
		service = TunnelService
	}
	if err = m.Client.RouteTunnel(ctx, tunnel.ID, d.Hostname, service); err != nil {
		return d, err
	}
	if d.DNSID, err = m.Client.CreateCNAME(ctx, d.Hostname, tunnel.ID); err != nil {
		return d, err
	}
	if d.AppID, err = m.Client.CreateAccessApp(ctx, resource, d.Hostname); err != nil {
		return d, err
	}
	if d.PolicyID, err = m.Client.AllowServiceToken(ctx, d.AppID, resource+"-mesh"); err != nil {
		return d, err
	}
	return d, nil
}

// Teardown deletes what Device records. A resource that is already gone counts as removed; the first
// real failure is returned after every other deletion has been tried.
func (m *Mesh) Teardown(ctx context.Context, d Device) error {
	var first error
	step := func(id string, remove func(context.Context, string) error) {
		if id == "" {
			return
		}
		if err := remove(ctx, id); err != nil && !IsNotFound(err) && first == nil {
			first = err
		}
	}
	step(d.AppID, m.Client.DeleteAccessApp)
	step(d.DNSID, m.Client.DeleteDNS)
	step(d.TunnelID, m.Client.DeleteTunnel)
	return first
}

// Setup provisions a device and records it. The mesh service token is created on first use and kept in
// the state file so later devices present the same one. The returned Device still carries the tunnel
// token; it is not written to the state file.
func (m *Mesh) Setup(ctx context.Context, state *StateFile, name string) (Device, SavedToken, error) {
	st, err := state.Load()
	if err != nil {
		return Device{}, SavedToken{}, err
	}
	if _, ok := st.Devices[name]; ok {
		return Device{}, SavedToken{}, fmt.Errorf("设备 %q 已有 Cloudflare 资源；先用 remove 删除再重建", name)
	}
	// A recorded token that was deleted elsewhere would make Access reject every policy. With no device
	// yet it is simply replaced; devices already admitted by it would be left holding a dead secret, so
	// those must be removed first.
	if st.ServiceToken != nil {
		exists, err := m.Client.ServiceTokenExists(ctx, st.ServiceToken.ID)
		if err != nil {
			return Device{}, SavedToken{}, err
		}
		switch {
		case exists:
		case len(st.Devices) > 0:
			return Device{}, SavedToken{}, errors.New("网格服务令牌已在 Cloudflare 被删除；先用 remove 删除现有设备，再重新 setup")
		default:
			st.ServiceToken = nil
		}
	}
	createdToken := false
	if st.ServiceToken == nil {
		token, err := m.NewServiceToken(ctx, "mesh")
		if err != nil {
			return Device{}, SavedToken{}, err
		}
		st.ServiceToken = &SavedToken{ID: token.ID, ClientID: token.ClientID, Secret: token.Secret}
		createdToken = true
		if err := state.Save(st); err != nil {
			_ = m.Client.DeleteServiceToken(context.WithoutCancel(ctx), token.ID)
			return Device{}, SavedToken{}, err
		}
		createdToken = true
	}
	// A token this call created is only useful with a device; without one nothing could remove it later.
	dropToken := func() {
		if !createdToken {
			return
		}
		bg := context.WithoutCancel(ctx)
		if err := m.Client.DeleteServiceToken(bg, st.ServiceToken.ID); err == nil || IsNotFound(err) {
			st.ServiceToken = nil
			_ = state.Save(st)
		}
	}
	d, err := m.ProvisionDevice(ctx, name, st.ServiceToken.ID)
	if err != nil {
		dropToken()
		return Device{}, SavedToken{}, err
	}
	recorded := d
	recorded.TunnelToken = ""
	st.Devices[name] = recorded
	if err := state.Save(st); err != nil {
		_ = m.Teardown(context.WithoutCancel(ctx), d)
		delete(st.Devices, name)
		dropToken()
		return Device{}, SavedToken{}, err
	}
	return d, *st.ServiceToken, nil
}

// Remove deletes a device's resources. The mesh service token goes with the last device.
func (m *Mesh) Remove(ctx context.Context, state *StateFile, name string) error {
	st, err := state.Load()
	if err != nil {
		return err
	}
	d, ok := st.Devices[name]
	if !ok {
		return fmt.Errorf("没有名为 %q 的设备记录", name)
	}
	if err := m.Teardown(ctx, d); err != nil {
		return err
	}
	delete(st.Devices, name)
	if len(st.Devices) == 0 && st.ServiceToken != nil {
		if err := m.Client.DeleteServiceToken(ctx, st.ServiceToken.ID); err != nil && !IsNotFound(err) {
			_ = state.Save(st)
			return err
		}
		st.ServiceToken = nil
	}
	return state.Save(st)
}
