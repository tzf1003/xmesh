package control

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// FileName is written into the node's state directory while the channel is up.
const FileName = v1.ControlFileName

// Bound is where a listener ended up: the transport and its address (socket path or pipe name).
type Bound struct {
	Transport v1.Transport
	Address   string
}

// Endpoint is control.json plus the directory it was read from.
type Endpoint struct {
	v1.ControlEndpointFile
	// Dir is the state directory the file was read from. Not serialised.
	Dir string `json:"-"`
}

// WriteEndpoint stores e atomically with owner-only permission. On Windows each SID in readers is
// granted read access to the written file (the state directory itself stays private), so the UI of
// the user who installed a SYSTEM service can find the channel; the grant is repeated on every
// write because the rename brings in a new file.
func WriteEndpoint(dir string, e v1.ControlEndpointFile, readers ...string) error {
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, FileName+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Chmod(0600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	final := filepath.Join(dir, FileName)
	if err == nil {
		err = os.Rename(name, final)
		if err == nil {
			if err = privdir.GrantRead(final, readers...); err != nil {
				_ = os.Remove(final)
			}
			return err
		}
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// RemoveEndpoint deletes control.json if it still describes the server that wrote owner: the same
// process and the same start time. A replacement server in this process started later and must keep
// its file.
func RemoveEndpoint(dir string, owner v1.ControlEndpointFile) {
	if e, err := ReadEndpoint(dir); err == nil && e.PID == owner.PID && e.StartedAt.Equal(owner.StartedAt.Time) {
		_ = os.Remove(filepath.Join(dir, FileName))
	}
}
func ReadEndpoint(dir string) (Endpoint, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return Endpoint{}, err
	}
	var e Endpoint
	if err = json.Unmarshal(raw, &e.ControlEndpointFile); err != nil {
		return Endpoint{}, err
	}
	if e.ControlEndpointFile.Endpoint == "" || (e.Transport != v1.TransportUnix && e.Transport != v1.TransportPipe) {
		return Endpoint{}, errors.New("control.json 无效")
	}
	e.Dir = dir
	return e, nil
}

// Address is the socket path or pipe name to dial.
func (e Endpoint) Address() string { return e.ControlEndpointFile.Endpoint }

// SystemStateRoot is where a Windows service running as SYSTEM keeps its node
// directories, %ProgramData%\xmesh, so a user's desktop UI can find them. It is
// empty on other platforms.
func SystemStateRoot() string { return SystemStateRootFor(runtime.GOOS, os.Getenv) }

// SystemStateRootFor is SystemStateRoot for an explicit platform and environment.
func SystemStateRootFor(goos string, getenv func(string) string) string {
	if goos != "windows" {
		return ""
	}
	base := strings.TrimRight(getenv("ProgramData"), `\/`)
	if base == "" {
		base = `C:\ProgramData`
	}
	return base + `\xmesh`
}

// Discover lists the endpoints of every node of the current user, i.e.
// <UserConfigDir>/xmesh/*/control.json, plus those of SYSTEM services under
// %ProgramData%\xmesh on Windows. Stale files are returned too; a client finds
// out by connecting.
func Discover() ([]Endpoint, error) {
	roots := []string{}
	userRoot, userErr := os.UserConfigDir()
	if userErr == nil {
		roots = append(roots, filepath.Join(userRoot, "xmesh"))
	}
	if system := SystemStateRoot(); system != "" {
		roots = append(roots, system)
	}
	if len(roots) == 0 {
		return nil, userErr
	}
	out := []Endpoint{}
	seen := map[string]bool{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			dir := filepath.Join(root, ent.Name())
			key := dir
			if runtime.GOOS == "windows" {
				key = strings.ToLower(dir)
			}
			if seen[key] {
				continue // the same directory reached through two roots
			}
			seen[key] = true
			if e, err := ReadEndpoint(dir); err == nil {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}
