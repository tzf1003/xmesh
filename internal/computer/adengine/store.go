package adengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Store is the device's agent-desktop engine directory,
// <state>/engines/agent-desktop. Each installed version has its own
// subdirectory. The active, previous and pending files each name a version; a
// missing or empty pointer means the bundled copy.
type Store struct {
	Root string
}

// NewStore returns the engine store under a Host state directory.
func NewStore(stateDir string) Store {
	return Store{Root: filepath.Join(stateDir, "engines", EngineAgentDesktop)}
}

// Ensure creates the private store directory.
func (s Store) Ensure() error { return os.MkdirAll(s.Root, 0o700) }

// VersionDir is where a version is installed.
func (s Store) VersionDir(version string) string { return filepath.Join(s.Root, version) }

const (
	activeFile   = "active"
	previousFile = "previous"
	pendingFile  = "pending"
	policyFile   = "autoupdate.json"
)

func (s Store) readPointer(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.Root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", nil
	}
	if _, err := ParseVersion(v); err != nil {
		return "", fmt.Errorf("%s 指针无效: %w", name, err)
	}
	return v, nil
}

// writePointer replaces a pointer atomically. An empty version removes it.
func (s Store) writePointer(name, version string) error {
	p := filepath.Join(s.Root, name)
	if version == "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := s.Ensure(); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(version+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Active returns the version the Host runs, or "" for the bundled copy.
func (s Store) Active() (string, error) { return s.readPointer(activeFile) }

// Previous returns the version a rollback would restore, or "" for the bundled copy.
func (s Store) Previous() (string, error) { return s.readPointer(previousFile) }

// Switch makes version active and keeps the former active version as previous.
func (s Store) Switch(version string) error {
	active, err := s.Active()
	if err != nil {
		return err
	}
	if active == version {
		return nil
	}
	if err := s.writePointer(previousFile, active); err != nil {
		return err
	}
	return s.writePointer(activeFile, version)
}

// Swap exchanges active and previous. It is the rollback step.
func (s Store) Swap() error {
	active, err := s.Active()
	if err != nil {
		return err
	}
	previous, err := s.Previous()
	if err != nil {
		return err
	}
	if active == "" && previous == "" {
		return errors.New("没有可回滚的 agent-desktop 版本")
	}
	if err := s.writePointer(activeFile, previous); err != nil {
		return err
	}
	return s.writePointer(previousFile, active)
}

// Pending returns an auto-applied version that has not yet passed a Host start.
func (s Store) Pending() (string, error) { return s.readPointer(pendingFile) }

// SetPending records or clears (with "") the pending version.
func (s Store) SetPending(version string) error { return s.writePointer(pendingFile, version) }

// suspendAfter is the number of consecutive automatic failures that stops
// automatic updates until a person runs an update by hand.
const suspendAfter = 2

// Policy is the auto-update record.
type Policy struct {
	ConsecutiveFailures int       `json:"consecutive_failures"`
	FailedVersions      []string  `json:"failed_versions,omitempty"`
	Suspended           bool      `json:"suspended"`
	LastError           string    `json:"last_error,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// LoadPolicy reads the auto-update record; a missing file is an empty record.
func (s Store) LoadPolicy() (Policy, error) {
	var p Policy
	b, err := os.ReadFile(filepath.Join(s.Root, policyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return Policy{}, fmt.Errorf("autoupdate.json 无效: %w", err)
	}
	return p, nil
}

// SavePolicy writes the auto-update record and derives Suspended from it.
func (s Store) SavePolicy(p Policy) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	p.UpdatedAt = time.Now().UTC()
	p.Suspended = p.ConsecutiveFailures >= suspendAfter
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Root, policyFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Root, policyFile))
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}
