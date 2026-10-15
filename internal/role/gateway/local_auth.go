package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/oauth"
)

// LocalAuthentication authenticates each request against the current PAT files.
// A configured PAT set requires a bearer even on the loopback listener.
func LocalAuthentication(store *config.Store) func(*http.Request) (Principal, error) {
	return func(r *http.Request) (Principal, error) {
		fail := func() (Principal, error) { return Principal{}, fmt.Errorf("Gateway 身份无效") }
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || host != "127.0.0.1" {
			return fail()
		}
		pats := store.Get().Gateway.PATs
		headers := r.Header.Values("Authorization")
		if len(pats) == 0 && len(headers) == 0 {
			return Principal{Local: true}, nil
		}
		if len(headers) != 1 {
			return fail()
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 32 || len(parts[1]) > 4096 {
			return fail()
		}
		presented := sha256.Sum256([]byte(parts[1]))
		matched := ""
		var scopes []string
		for id, pat := range pats {
			file, err := os.Open(pat.TokenFile)
			if err != nil {
				return fail()
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
				file.Close()
				return fail()
			}
			// Bound the read even if a concurrent writer grows the file.
			buf, err := io.ReadAll(io.LimitReader(file, 4097))
			file.Close()
			if err != nil || len(buf) > 4096 {
				return fail()
			}
			secret := strings.TrimSpace(string(buf))
			if len(secret) < 32 || strings.ContainsAny(secret, " \t\r\n") {
				return fail()
			}
			stored := sha256.Sum256([]byte(secret))
			if subtle.ConstantTimeCompare(presented[:], stored[:]) == 1 {
				if matched != "" {
					return fail()
				}
				matched, scopes = id, pat.Scopes
			}
		}
		if matched == "" {
			return fail()
		}
		expanded, err := oauth.Expand(scopes)
		if err != nil {
			return fail()
		}
		return Principal{CredentialID: "pat:" + matched, Scopes: expanded}, nil
	}
}
