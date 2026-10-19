package app

import (
	"os"
	"path/filepath"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/oauth"
)

// ingressAdmin lets the control channel manage the running ingress's OAuth server.
type ingressAdmin struct {
	auth         *oauth.Server
	passwordFile string
}

var _ control.IngressAdmin = (*ingressAdmin)(nil)

func (a *ingressAdmin) Issuer() string { return a.auth.Issuer() }

func (a *ingressAdmin) Clients() []v2.IngressClient {
	out := []v2.IngressClient{}
	for _, c := range a.auth.Clients() {
		out = append(out, v2.IngressClient{ClientID: c.ID, ClientName: c.Name, RedirectURIs: append([]string{}, c.Redirects...), Scopes: append([]string{}, c.Scopes...), TokenEndpointAuthMethod: c.Method})
	}
	return out
}

func (a *ingressAdmin) RevokeClient(id string) (bool, error) { return a.auth.RevokeClient(id) }

// RotatePassword rewrites the password file (0600, atomic) and only then switches the server over,
// so a failed write leaves the old password in force.
func (a *ingressAdmin) RotatePassword(password string) error {
	if a.passwordFile == "" {
		return &control.PasswordSourceError{}
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.passwordFile), ".oauth-password-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.WriteString(password + "\n")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Close()
	}
	if err == nil {
		err = os.Rename(tmp.Name(), a.passwordFile)
	}
	if err != nil {
		return err
	}
	a.auth.SetPassword(password)
	return nil
}
