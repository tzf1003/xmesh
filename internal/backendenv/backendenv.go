// Package backendenv keeps the environment variables of stdio backends (API keys and the like) in
// one private file, apart from the configuration. The configuration file is read, diffed, shown
// and exported by many paths; this file is read only by the kernel when it starts a backend, and
// the control channel reports the variable names but never a value.
package backendenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// FileName is the file inside the node's private state directory.
const FileName = "backend-env.json"

const (
	// MaxVars bounds the variables of one backend; MaxValueBytes bounds one value.
	MaxVars       = 64
	MaxValueBytes = 8192
)

var (
	namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// backendPattern is the backend name syntax of the configuration.
	backendPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
)

// ValidBackend reports whether name is a legal backend name.
func ValidBackend(name string) bool { return backendPattern.MatchString(name) }

// ValidName reports whether name is a legal environment variable name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ValidValue reports whether value can be passed to a process: bounded, no NUL.
func ValidValue(value string) bool {
	return len(value) <= MaxValueBytes && !strings.ContainsRune(value, 0)
}

// ErrTooMany is returned when a patch would leave a backend with more than MaxVars variables.
var ErrTooMany = errors.New("too many variables")

type document struct {
	Backends map[string]map[string]string `json:"backends"`
}

// Store is the file and its parsed contents. All methods are safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	path   string
	vars   map[string]map[string]string
	digest [32]byte
}

// Open reads path; a missing file is an empty store. A file that does not parse is an error and
// is left untouched, so a damaged file is never silently overwritten.
func Open(path string) (*Store, error) {
	s := &Store{path: path, vars: map[string]map[string]string{}}
	if _, err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path is the file this store keeps.
func (s *Store) Path() string { return s.path }

func parse(raw []byte) (map[string]map[string]string, error) {
	var doc document
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("backend env file is not valid")
	}
	out := map[string]map[string]string{}
	for backend, vars := range doc.Backends {
		if !ValidBackend(backend) || len(vars) > MaxVars {
			return nil, errors.New("backend env file is not valid")
		}
		for name, value := range vars {
			if !ValidName(name) || !ValidValue(value) {
				return nil, errors.New("backend env file is not valid")
			}
		}
		if len(vars) > 0 {
			out[backend] = vars
		}
	}
	return out, nil
}

// reload reads the file and reports whether its contents differ from what the store held. The
// caller holds s.mu, or owns s alone.
func (s *Store) reload() (bool, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = nil, nil
	}
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(raw)
	if digest == s.digest {
		return false, nil
	}
	vars := map[string]map[string]string{}
	if raw != nil {
		if vars, err = parse(raw); err != nil {
			return false, err
		}
	}
	s.digest = digest
	changed := !equal(vars, s.vars)
	s.vars = vars
	return changed, nil
}

func equal(a, b map[string]map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for backend, vars := range a {
		other, ok := b[backend]
		if !ok || len(vars) != len(other) {
			return false
		}
		for name, value := range vars {
			if v, ok := other[name]; !ok || v != value {
				return false
			}
		}
	}
	return true
}

// Reload re-reads the file after an edit made outside the kernel. A file that does not parse
// keeps the variables in effect and returns the error.
func (s *Store) Reload() (changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reload()
}

// Snapshot is a deep copy of every backend's variables.
func (s *Store) Snapshot() map[string]map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.vars)
}

func clone(in map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(in))
	for backend, vars := range in {
		copied := make(map[string]string, len(vars))
		for name, value := range vars {
			copied[name] = value
		}
		out[backend] = copied
	}
	return out
}

// Names are the variable names of one backend, sorted. It never returns a value.
func (s *Store) Names(backend string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return namesOf(s.vars[backend])
}

func namesOf(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for name := range vars {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Patch sets and removes variables of one backend and writes the file. It starts from the file as
// it is now, so an edit made outside the kernel is not lost. The names left are returned.
func (s *Store) Patch(backend string, set map[string]string, unset []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.reload(); err != nil {
		return nil, err
	}
	next := clone(s.vars)
	vars := next[backend]
	if vars == nil {
		vars = map[string]string{}
	}
	for _, name := range unset {
		delete(vars, name)
	}
	for name, value := range set {
		vars[name] = value
	}
	if len(vars) > MaxVars {
		return nil, ErrTooMany
	}
	if len(vars) == 0 {
		delete(next, backend)
	} else {
		next[backend] = vars
	}
	if err := s.write(next); err != nil {
		return nil, err
	}
	return namesOf(vars), nil
}

// Clear removes every variable of one backend. It is not an error if there were none.
func (s *Store) Clear(backend string) error {
	_, err := s.Prune(func(name string) bool { return name != backend })
	return err
}

// Prune drops the variables of every backend for which keep returns false and reports whether
// anything was removed.
func (s *Store) Prune(keep func(backend string) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.reload(); err != nil {
		return false, err
	}
	next := clone(s.vars)
	removed := false
	for backend := range next {
		if !keep(backend) {
			delete(next, backend)
			removed = true
		}
	}
	if !removed {
		return false, nil
	}
	return true, s.write(next)
}

// write replaces the file atomically with mode 0600 and adopts next. The caller holds s.mu.
func (s *Store) write(next map[string]map[string]string) error {
	raw, err := json.MarshalIndent(document{Backends: next}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+".tmp-")
	if err != nil {
		return fmt.Errorf("write backend env file: %w", err)
	}
	name := tmp.Name()
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Chmod(0600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, s.path)
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write backend env file: %w", err)
	}
	s.vars = next
	s.digest = sha256.Sum256(raw)
	return nil
}
