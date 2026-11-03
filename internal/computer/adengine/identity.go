package adengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Where an executable came from. Managed copies live under the device state
// directory and carry a receipt written by a verified install. Bundled copies
// ship beside xmesh (or come from --agent-desktop) and are checked against the
// manifest pin for the current platform.
const (
	SourceManaged = "managed"
	SourceBundled = "bundled"
)

// Reason codes for an executable that cannot be trusted or is missing.
const (
	ProblemNotInstalled  = "not_installed"
	ProblemPointer       = "pointer_invalid"
	ProblemPlatform      = "platform_not_in_manifest"
	ProblemBinaryHash    = "binary_hash_mismatch"
	ProblemHelperMissing = "helper_missing"
	ProblemHelperHash    = "helper_hash_mismatch"
	ProblemReceipt       = "receipt_invalid"
)

// Identity is the verdict on one agent-desktop executable.
type Identity struct {
	Path   string
	Source string
	// Found reports that the executable exists, whether or not it verifies.
	Found bool
	// Version is the release the SHA-256 values prove, or "" when unproven.
	Version string
	// Problem is a reason code when the executable must not be used.
	Problem string
}

// Receipt records what a verified install put on disk.
type Receipt struct {
	Version     string        `json:"version"`
	Platform    string        `json:"platform"`
	Integrity   string        `json:"tarball_integrity"`
	Binary      ReceiptFile   `json:"binary"`
	Helpers     []ReceiptFile `json:"helpers"`
	InstalledAt time.Time     `json:"installed_at"`
}

// ReceiptFile is an installed file name and its verified SHA-256.
type ReceiptFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

const receiptName = "receipt.json"

// IdentifyBundled verifies an executable against the manifest pin for key.
// Helpers are expected beside it under their install names.
func IdentifyBundled(m *Manifest, key, exe string) Identity {
	id := Identity{Path: exe, Source: SourceBundled}
	if !filepath.IsAbs(exe) || !regularFile(exe) {
		id.Problem = ProblemNotInstalled
		return id
	}
	id.Found = true
	p, ok := m.Platform(key)
	if !ok {
		id.Problem = ProblemPlatform
		return id
	}
	if err := checkSHA256(exe, p.Binary.SHA256); err != nil {
		id.Problem = ProblemBinaryHash
		return id
	}
	for _, h := range p.Helpers {
		if problem := checkHelper(filepath.Dir(exe), InstallName(h.Member, key), h.SHA256); problem != "" {
			id.Problem = problem
			return id
		}
	}
	id.Version = m.Version
	return id
}

// IdentifyManaged verifies an installed version against its receipt. The
// receipt keeps working after the manifest pin moves on, so a previous version
// can still be rolled back to.
func IdentifyManaged(s Store, version, key string) Identity {
	dir := s.VersionDir(version)
	id := Identity{Source: SourceManaged}
	raw, err := os.ReadFile(filepath.Join(dir, receiptName))
	if errors.Is(err, fs.ErrNotExist) {
		id.Problem = ProblemNotInstalled
		return id
	}
	if err != nil {
		id.Problem = ProblemReceipt
		return id
	}
	var r Receipt
	if json.Unmarshal(raw, &r) != nil || r.Version != version || r.Platform != key || !plainName(r.Binary.Name) {
		id.Problem = ProblemReceipt
		return id
	}
	if _, err := decodeSHA256(r.Binary.SHA256); err != nil {
		id.Problem = ProblemReceipt
		return id
	}
	id.Path = filepath.Join(dir, r.Binary.Name)
	if !regularFile(id.Path) {
		id.Problem = ProblemNotInstalled
		return id
	}
	id.Found = true
	if err := checkSHA256(id.Path, r.Binary.SHA256); err != nil {
		id.Problem = ProblemBinaryHash
		return id
	}
	for _, h := range r.Helpers {
		if !plainName(h.Name) {
			id.Problem = ProblemReceipt
			return id
		}
		if problem := checkHelper(dir, h.Name, h.SHA256); problem != "" {
			id.Problem = problem
			return id
		}
	}
	id.Version = version
	return id
}

func checkHelper(dir, name, want string) string {
	p := filepath.Join(dir, name)
	if !regularFile(p) {
		return ProblemHelperMissing
	}
	if err := checkSHA256(p, want); err != nil {
		return ProblemHelperHash
	}
	return ""
}

func checkSHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(want) {
		return fmt.Errorf("%s: sha256 与清单不一致", path)
	}
	return nil
}

func regularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// plainName rejects anything that is not a single file name in the version directory.
func plainName(n string) bool {
	return n != "" && n != "." && n != ".." && filepath.Base(n) == n && !strings.ContainsAny(n, `/\`)
}
