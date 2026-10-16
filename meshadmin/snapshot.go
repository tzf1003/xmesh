package meshadmin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// SnapshotStore keeps policy texts taken before each write.
type SnapshotStore interface {
	Save(Snapshot) (id string, err error)
	Load(id string) (Snapshot, error)
	// List returns snapshots newest first, without Text.
	List() ([]Snapshot, error)
}

var snapshotID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

func newSnapshotID(t time.Time) string {
	var r [4]byte
	_, _ = rand.Read(r[:])
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(r[:])
}

// DirStore stores each snapshot as a 0600 JSON file in a 0700 directory.
type DirStore struct{ Dir string }

func (d DirStore) Save(s Snapshot) (string, error) {
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return "", err
	}
	s.Time = time.Now().UTC()
	s.ID = newSnapshotID(s.Time)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(d.Dir, ".snap-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := os.Chmod(tmp.Name(), 0o600); err != nil && !isWindows() {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(d.Dir, s.ID+".json")); err != nil {
		return "", err
	}
	return s.ID, nil
}

func (d DirStore) Load(id string) (Snapshot, error) {
	if !snapshotID.MatchString(id) {
		return Snapshot{}, fmt.Errorf("%w: snapshot id %q", ErrInvalid, id)
	}
	b, err := os.ReadFile(filepath.Join(d.Dir, id+".json"))
	if os.IsNotExist(err) {
		return Snapshot{}, fmt.Errorf("%w: snapshot %s", ErrNotFound, id)
	}
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, fmt.Errorf("meshadmin: snapshot %s is corrupt: %w", id, err)
	}
	return s, nil
}

func (d DirStore) List() ([]Snapshot, error) {
	ents, err := os.ReadDir(d.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range ents {
		name := e.Name()
		if len(name) < 6 || name[len(name)-5:] != ".json" || !snapshotID.MatchString(name[:len(name)-5]) {
			continue
		}
		s, err := d.Load(name[:len(name)-5])
		if err != nil {
			continue
		}
		s.Text = ""
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// MemStore is an in-memory SnapshotStore for tests and one-shot callers.
type MemStore struct {
	mu    sync.Mutex
	items map[string]Snapshot
}

func (m *MemStore) Save(s Snapshot) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = map[string]Snapshot{}
	}
	s.Time = time.Now().UTC()
	s.ID = newSnapshotID(s.Time)
	m.items[s.ID] = s
	return s.ID, nil
}

func (m *MemStore) Load(id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.items[id]
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: snapshot %s", ErrNotFound, id)
	}
	return s, nil
}

func (m *MemStore) List() ([]Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Snapshot
	for _, s := range m.items {
		s.Text = ""
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}
