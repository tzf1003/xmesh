package control

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
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

// WriteEndpoint stores e atomically with owner-only permission.
func WriteEndpoint(dir string, e v1.ControlEndpointFile) error {
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
	if err == nil {
		err = os.Rename(name, filepath.Join(dir, FileName))
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// RemoveEndpoint deletes control.json if it still describes this process.
func RemoveEndpoint(dir string, pid int) {
	if e, err := ReadEndpoint(dir); err == nil && e.PID == pid {
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

// Discover lists the endpoints of every node of the current user, i.e.
// <UserConfigDir>/xmesh/*/control.json. Stale files are returned too; a client
// finds out by connecting.
func Discover() ([]Endpoint, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, "xmesh"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []Endpoint{}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if e, err := ReadEndpoint(filepath.Join(root, "xmesh", ent.Name())); err == nil {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}
