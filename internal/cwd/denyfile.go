package cwd

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"go.yaml.in/yaml/v3"
)

// denyFiles remembers, for each deny file, the list that last loaded. A file that stops loading
// (an edit in progress, a typo, a deletion) keeps that list in force instead of failing every
// call or widening the boundary; it is reported through DenyFailing until the file loads again.
var denyFiles = struct {
	sync.Mutex
	byPath   map[string]*denyState
	clock    uint64
	failures uint64
}{byPath: map[string]*denyState{}}

const inactiveDenyFileLimit = 64

type denyState struct {
	readMu     sync.Mutex
	good       []string
	goodDigest [32]byte
	loaded     bool
	failing    bool
	// badKey identifies the failure being reported, so the same bad content is counted once.
	badKey [33]byte
	refs   int
	used   uint64
}

// RetainDenyFile keeps the last good list available while a configuration uses path.
// The returned function releases that configuration's reference.
func RetainDenyFile(path string) func() {
	if path == "" {
		return func() {}
	}
	denyFiles.Lock()
	st := denyStateLocked(path)
	st.refs++
	denyFiles.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			denyFiles.Lock()
			defer denyFiles.Unlock()
			st.refs--
			trimDenyFilesLocked()
		})
	}
}

func denyStateLocked(path string) *denyState {
	st := denyFiles.byPath[path]
	if st == nil {
		st = &denyState{}
		denyFiles.byPath[path] = st
	}
	denyFiles.clock++
	st.used = denyFiles.clock
	return st
}

func trimDenyFilesLocked() {
	inactive := 0
	for _, st := range denyFiles.byPath {
		if st.refs == 0 {
			inactive++
		}
	}
	for inactive > inactiveDenyFileLimit {
		var oldest string
		var used uint64
		for path, st := range denyFiles.byPath {
			if st.refs == 0 && (oldest == "" || st.used < used) {
				oldest, used = path, st.used
			}
		}
		delete(denyFiles.byPath, oldest)
		inactive--
	}
}

// loadDenyFile returns the entries of the deny file at path. The file is read on every call, so an
// edit takes effect on the next one. When the file does not load, the list that last loaded is
// returned instead; only a file that has never loaded is an error, so the boundary is never
// opened by a file that cannot be read.
func loadDenyFile(path string) ([]string, error) {
	return loadDenyFileWith(path, os.ReadFile)
}

func loadDenyFileWith(path string, read func(string) ([]byte, error)) ([]string, error) {
	denyFiles.Lock()
	st := denyStateLocked(path)
	st.refs++
	denyFiles.Unlock()
	// Reads of one path stay ordered; a slow path never holds the shared cache lock.
	st.readMu.Lock()
	defer st.readMu.Unlock()
	raw, readErr := read(path)
	var digest [32]byte
	var entries []string
	var parseErr error
	if readErr == nil {
		digest = sha256.Sum256(raw)
		denyFiles.Lock()
		unchanged := st.loaded && digest == st.goodDigest
		denyFiles.Unlock()
		if !unchanged {
			entries, parseErr = parseDenyList(raw)
		}
	}
	denyFiles.Lock()
	defer func() { st.refs--; trimDenyFilesLocked(); denyFiles.Unlock() }()
	if readErr == nil && st.loaded && digest == st.goodDigest {
		st.failing = false
		return st.good, nil
	}
	var failure error
	if readErr != nil {
		failure = fmt.Errorf("读取禁止目录文件: %w", readErr)
	} else if parseErr != nil {
		failure = parseErr
	} else {
		st.good, st.goodDigest, st.loaded, st.failing = entries, digest, true, false
		return entries, nil
	}
	key := [33]byte{}
	if readErr == nil {
		key[0] = 1
		copy(key[1:], digest[:])
	}
	if !st.failing || st.badKey != key {
		denyFiles.failures++
		slog.Warn("policy_reload_failed", "source", "deny_file")
	}
	st.failing, st.badKey = true, key
	if st.loaded {
		return st.good, nil
	}
	return nil, failure
}

// parseDenyList reads a YAML list of strings. An empty list is written as [], so a file with no
// content at all is a half-written one and is refused.
func parseDenyList(raw []byte) ([]string, error) {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("禁止目录文件不是字符串列表: %w", err)
	}
	list, ok := doc.([]any)
	if !ok {
		return nil, fmt.Errorf("禁止目录文件不是字符串列表（空列表请写成 []）")
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("禁止目录文件不是字符串列表")
		}
		out = append(out, s)
	}
	return out, nil
}

// DenyFailing reports whether the deny file at path currently fails to load, so the list that
// last loaded is still in force. It clears once the file loads again.
func DenyFailing(path string) bool {
	if path == "" {
		return false
	}
	// Read the file again, so the answer is current even when no tool call has used it lately.
	_, _ = loadDenyFile(path)
	denyFiles.Lock()
	defer denyFiles.Unlock()
	st := denyFiles.byPath[path]
	return st != nil && st.failing
}

// DenyFailures counts the distinct failures to load deny files since the process started.
func DenyFailures() uint64 {
	denyFiles.Lock()
	defer denyFiles.Unlock()
	return denyFiles.failures
}
