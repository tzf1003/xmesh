package cfadmin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// State records what this machine created so it can be removed later and the mesh service token is
// reused for every device. It holds the service token secret, so the file is private to the owner.
type State struct {
	ServiceToken *SavedToken       `json:"service_token,omitempty"`
	Devices      map[string]Device `json:"devices,omitempty"`
}

// SavedToken is a service token with its secret.
type SavedToken struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id"`
	Secret   string `json:"secret"`
}

func (t SavedToken) String() string {
	return "SavedToken{id=" + t.ID + " client_id=" + t.ClientID + " secret=<redacted>}"
}
func (t SavedToken) GoString() string { return t.String() }

// StateFile is a State on disk. Writes replace the file atomically.
type StateFile struct {
	Path string
	mu   sync.Mutex
}

func (f *StateFile) Load() (State, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Devices: map[string]Device{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if json.Unmarshal(raw, &s) != nil {
		return State{}, errors.New("Cloudflare 状态文件已损坏")
	}
	if s.Devices == nil {
		s.Devices = map[string]Device{}
	}
	return s, nil
}

func (f *StateFile) Save(s State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}
