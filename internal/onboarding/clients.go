package onboarding

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// ClientInfo describes a supported local MCP client and its user configuration.
type ClientInfo struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Problem   string `json:"problem,omitempty"`
}

// ClientChange previews the configuration path, hash, and change summary.
// BeforeHash binds approval to the exact file inspected by PlanClients,
// including whether it existed.
type ClientChange struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	BeforeHash string `json:"before_hash"`
	Exists     bool   `json:"exists"`
	Conflict   bool   `json:"conflict"`
	Summary    string `json:"summary"`
}

// ClientReceipt belongs in the private transaction journal. It contains only
// paths and hashes; the credentials themselves remain in the client file.
type ClientReceipt struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	BackupPath string `json:"backup_path"`
	AfterHash  string `json:"after_hash"`
	BeforeHash string `json:"before_hash"`
	Existed    bool   `json:"existed"`
}

// ClientPaths resolves the clients' user-level configuration locations. Claude
// Code relocates its global .claude.json alongside CLAUDE_CONFIG_DIR state.
func ClientPaths(home string, env func(string) string) map[string]string {
	codexHome := env("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	claudeHome := env("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = home
	}
	return map[string]string{
		"codex":       filepath.Join(codexHome, "config.toml"),
		"claude-code": filepath.Join(claudeHome, ".claude.json"),
	}
}

func currentClientPaths() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("resolve client configuration home")
	}
	paths := ClientPaths(home, os.Getenv)
	for id, path := range paths {
		paths[id], err = filepath.Abs(path)
		if err != nil {
			return nil, errors.New("resolve client configuration path")
		}
	}
	return paths, nil
}

var clientVersion = regexp.MustCompile(`\b[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}\b`)

// InspectClients only invokes the clients' local --version commands. A bounded
// version string is returned so arbitrary command output cannot enter a report.
func InspectClients() []ClientInfo {
	paths, pathErr := currentClientPaths()
	result := make([]ClientInfo, 0, 2)
	for _, id := range []string{"codex", "claude-code"} {
		info := ClientInfo{ID: id, Path: paths[id]}
		if pathErr != nil {
			info.Problem = pathErr.Error()
		}
		executable := id
		if id == "claude-code" {
			executable = "claude"
		}
		command, err := exec.LookPath(executable)
		if err != nil {
			info.Problem = "Client executable was not found on PATH."
		} else {
			info.Installed = true
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var output limitedClientOutput
			cmd := exec.CommandContext(ctx, command, "--version")
			cmd.WaitDelay = 250 * time.Millisecond
			cmd.Stdout, cmd.Stderr = &output, io.Discard
			err = cmd.Run()
			cancel()
			if err != nil {
				info.Problem = "Client version could not be read."
			} else {
				info.Version = clientVersion.FindString(output.String())
				if info.Version == "" {
					info.Problem = "Client version was not recognized."
				}
			}
		}
		result = append(result, info)
	}
	return result
}

type limitedClientOutput struct{ bytes.Buffer }

func (b *limitedClientOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

// PlanClients validates all selected files before presenting a preview.
func PlanClients(ids []string, endpoint string) ([]ClientChange, error) {
	if err := validClientEndpoint(endpoint); err != nil {
		return nil, err
	}
	paths, err := currentClientPaths()
	if err != nil {
		return nil, err
	}
	changes := make([]ClientChange, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		path, ok := paths[id]
		if !ok {
			return nil, errors.New("unsupported MCP client")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		data, exists, err := readClientFile(path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s configuration: %w", id, err)
		}
		entry, err := clientEntry(id, data, exists)
		if err != nil {
			return nil, err
		}
		conflict := entry != nil && !clientEntryMatches(id, entry, endpoint)
		summary := "Add the xmesh HTTP MCP connection and its private authorization header."
		if entry != nil {
			summary = "Update the xmesh HTTP MCP connection and its private authorization header."
		}
		if conflict {
			summary = "Replace the existing xmesh MCP entry with the selected connection and private authorization header."
		}
		changes = append(changes, ClientChange{ID: id, Path: path, BeforeHash: hashClientFile(data, exists), Exists: exists, Conflict: conflict, Summary: summary})
	}
	return changes, nil
}

// ApplyClient backs up and merges one approved client entry. A receipt returned
// with an error means replacement completed and can be compensated by rollback.
func ApplyClient(change ClientChange, endpoint, token string) (*ClientReceipt, error) {
	return ApplyClientPrepared(change, endpoint, token, nil)
}

// ApplyClientPrepared durably prepares rollback metadata before replacing a
// client file. The caller must save the receipt in its private transaction
// journal inside prepare; returning an error keeps the client file intact.
func ApplyClientPrepared(change ClientChange, endpoint, token string, prepare func(ClientReceipt) error) (*ClientReceipt, error) {
	if err := validClientEndpoint(endpoint); err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("a valid client authorization token is required")
	}
	if err := validateClientChangePath(change.ID, change.Path); err != nil {
		return nil, err
	}
	if err := ensureClientParent(change.Path); err != nil {
		return nil, err
	}
	lock, err := lockClientPath(change.Path)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	before, exists, err := readClientFile(change.Path)
	if err != nil {
		return nil, err
	}
	if exists != change.Exists || hashClientFile(before, exists) != change.BeforeHash {
		return nil, errors.New("client configuration changed after preview; preview it again")
	}
	after, err := mergeClientConfig(change.ID, before, exists, endpoint, token)
	if err != nil {
		return nil, err
	}
	receipt := &ClientReceipt{ID: change.ID, Path: change.Path, BeforeHash: change.BeforeHash, Existed: exists, AfterHash: hashClientFile(after, true)}
	if exists {
		receipt.BackupPath, err = writeClientBackup(change.Path, before)
		if err != nil {
			return nil, err
		}
	}
	if prepare != nil {
		if err := prepare(*receipt); err != nil {
			return nil, errors.New("save client installation recovery record")
		}
	}
	if err := replaceClientFile(change.Path, after, change.BeforeHash); err != nil {
		// A completed backup remains useful for recovery even if replacement fails.
		if installed, present, readErr := readClientFile(change.Path); readErr == nil && present && hashClientFile(installed, true) == receipt.AfterHash {
			return receipt, err
		}
		return nil, err
	}
	if err := VerifyClient(*receipt, endpoint); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// VerifyClient checks both the installed bytes and the saved MCP entry.
func VerifyClient(receipt ClientReceipt, endpoint string) error {
	if err := validClientEndpoint(endpoint); err != nil {
		return err
	}
	if err := validateClientReceipt(receipt); err != nil {
		return err
	}
	data, exists, err := readClientFile(receipt.Path)
	if err != nil {
		return err
	}
	if !exists || hashClientFile(data, true) != receipt.AfterHash {
		return errors.New("saved client configuration differs from the installed configuration")
	}
	entry, err := clientEntry(receipt.ID, data, true)
	if err != nil {
		return err
	}
	if !clientEntryMatches(receipt.ID, entry, endpoint) {
		return errors.New("saved xmesh MCP entry could not be verified")
	}
	return verifyPrivateClientFile(receipt.Path)
}

// RollbackClient restores only the exact file written by this transaction.
// Repeated calls succeed once the original state has been restored.
func RollbackClient(receipt ClientReceipt) error {
	if err := validateClientReceipt(receipt); err != nil {
		return err
	}
	if err := checkClientPath(receipt.Path); err != nil {
		return err
	}
	if !receipt.Existed {
		if _, exists, err := readClientFile(receipt.Path); err != nil {
			return err
		} else if !exists {
			return nil
		}
	}
	lock, err := lockClientPath(receipt.Path)
	if err != nil {
		return err
	}
	defer lock.Close()
	var original []byte
	if receipt.Existed {
		var exists bool
		original, exists, err = readClientFile(receipt.BackupPath)
		if err != nil || !exists || hashClientFile(original, true) != receipt.BeforeHash {
			return errors.New("client backup could not be authenticated")
		}
	}
	current, exists, err := readClientFile(receipt.Path)
	if err != nil {
		return err
	}
	currentHash := hashClientFile(current, exists)
	if currentHash == receipt.BeforeHash && exists == receipt.Existed {
		return nil
	}
	if !exists || currentHash != receipt.AfterHash {
		return errors.New("client configuration changed after installation; rollback requires review")
	}
	if receipt.Existed {
		return replaceClientFile(receipt.Path, original, receipt.AfterHash)
	}
	// Recheck immediately before unlink, while the cooperating-writer lock is held.
	if err := checkClientHash(receipt.Path, receipt.AfterHash); err != nil {
		return err
	}
	if err := os.Remove(receipt.Path); err != nil {
		return errors.New("remove installed client configuration")
	}
	return syncClientDir(filepath.Dir(receipt.Path))
}

func validClientEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("client MCP endpoint must be a loopback HTTP URL")
	}
	host := u.Hostname()
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return errors.New("client MCP endpoint must be a loopback HTTP URL")
	}
	return nil
}

func hashClientFile(data []byte, exists bool) string {
	h := sha256.New()
	if exists {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func validateClientChangePath(id, path string) error {
	paths, err := currentClientPaths()
	if err != nil {
		return err
	}
	if paths[id] == "" || paths[id] != path || !filepath.IsAbs(path) {
		return errors.New("client configuration path does not match the selected client")
	}
	return checkClientPath(path)
}

func validateClientReceipt(receipt ClientReceipt) error {
	if (receipt.ID != "codex" && receipt.ID != "claude-code") || !filepath.IsAbs(receipt.Path) {
		return errors.New("invalid client installation receipt")
	}
	for _, hash := range []string{receipt.BeforeHash, receipt.AfterHash} {
		b, err := hex.DecodeString(hash)
		if err != nil || len(b) != sha256.Size {
			return errors.New("invalid client installation receipt")
		}
	}
	if receipt.Existed {
		if filepath.Dir(receipt.BackupPath) != filepath.Dir(receipt.Path) || !strings.HasPrefix(receipt.BackupPath, receipt.Path+".xmesh-backup-") {
			return errors.New("invalid client backup path")
		}
	} else if receipt.BackupPath != "" || receipt.BeforeHash != hashClientFile(nil, false) {
		return errors.New("invalid client installation receipt")
	}
	return nil
}

const maxClientConfig = 8 << 20

func readClientFile(path string) ([]byte, bool, error) {
	if err := checkClientPath(path); err != nil {
		return nil, false, err
	}
	f, err := openClientFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("open client configuration")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > maxClientConfig {
		return nil, false, errors.New("client configuration must be a regular file of at most 8 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxClientConfig+1))
	if err != nil || len(b) > maxClientConfig {
		return nil, false, errors.New("read client configuration")
	}
	return b, true, nil
}

// Reject links in every existing path component; config replacement must stay
// within the path whose approval and backup were recorded.
func checkClientPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("client configuration requires an absolute clean path")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect client configuration path")
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 && !clientSystemPathAlias(current) {
			return errors.New("client configuration path contains a symbolic link")
		}
		if err == nil && current != path && !info.IsDir() && !clientSystemPathAlias(current) {
			return errors.New("client configuration parent is not a directory")
		}
		if parent := filepath.Dir(current); parent == current {
			break
		}
	}
	return nil
}

// macOS provides these root-owned filesystem aliases. Accepting them keeps
// native temporary and configuration paths usable while user-created links in
// the configuration path still require a real path for approval.
func clientSystemPathAlias(path string) bool {
	if runtime.GOOS != "darwin" || (path != "/var" && path != "/tmp" && path != "/etc") {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == filepath.Join("/private", filepath.Base(path))
}

func ensureClientParent(path string) error {
	if err := checkClientPath(path); err != nil {
		return err
	}
	var missing []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		_, err := os.Lstat(dir)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect client configuration directory")
		}
		missing = append(missing, dir)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := privdir.Ensure(missing[i]); err != nil {
			return errors.New("create private client configuration directory")
		}
	}
	return checkClientPath(path)
}

func lockClientPath(path string) (*os.File, error) {
	lockPath := path + ".xmesh.lock"
	if err := checkClientPath(lockPath); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(ctx, lockPath)
	if err != nil {
		return nil, errors.New("lock client configuration")
	}
	return lock, nil
}

func writeClientBackup(path string, data []byte) (string, error) {
	f, err := createRandomClientFile(path + ".xmesh-backup-")
	if err != nil {
		return "", errors.New("create private client backup")
	}
	name := f.Name()
	if err := finishClientFile(f, data); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := syncClientDir(filepath.Dir(name)); err != nil {
		return "", err
	}
	return name, nil
}

func createRandomClientFile(prefix string) (*os.File, error) {
	for range 10 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		f, err := createPrivateClientFile(prefix + hex.EncodeToString(random[:]))
		if !errors.Is(err, os.ErrExist) {
			return f, err
		}
	}
	return nil, errors.New("allocate private client file")
}

func finishClientFile(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return errors.New("write private client configuration")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return errors.New("sync private client configuration")
	}
	if err := f.Close(); err != nil {
		return errors.New("close private client configuration")
	}
	return nil
}

func checkClientHash(path, expected string) error {
	current, exists, err := readClientFile(path)
	if err != nil {
		return err
	}
	if hashClientFile(current, exists) != expected {
		return errors.New("client configuration changed during update; preview it again")
	}
	return nil
}

func replaceClientFile(path string, data []byte, expected string) error {
	f, err := createRandomClientFile(filepath.Join(filepath.Dir(path), ".xmesh-client-"))
	if err != nil {
		return errors.New("create private client configuration")
	}
	name := f.Name()
	defer os.Remove(name)
	if err := finishClientFile(f, data); err != nil {
		return err
	}
	if err := checkClientHash(path, expected); err != nil {
		return err
	}
	if err := renameClientFile(name, path); err != nil {
		return errors.New("replace client configuration")
	}
	return syncClientDir(filepath.Dir(path))
}

func clientEntry(id string, data []byte, exists bool) (map[string]any, error) {
	if !exists {
		return nil, nil
	}
	if id == "codex" {
		var document map[string]any
		if err := toml.Unmarshal(data, &document); err != nil {
			return nil, errors.New("Codex configuration is not valid TOML")
		}
		servers, ok := document["mcp_servers"]
		if !ok {
			return nil, nil
		}
		serverMap, ok := servers.(map[string]any)
		if !ok {
			return nil, errors.New("Codex mcp_servers must be a table")
		}
		return entryObject(serverMap["xmesh"]), nil
	}
	if id != "claude-code" {
		return nil, errors.New("unsupported MCP client")
	}
	root, err := parseClientJSON(data)
	if err != nil {
		return nil, err
	}
	servers, err := jsonServerMap(root)
	if err != nil {
		return nil, err
	}
	if raw, ok := servers["xmesh"]; ok {
		var entry any
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, errors.New("Claude Code xmesh entry is not valid JSON")
		}
		if entry == nil {
			return map[string]any{"_invalid_entry": true}, nil
		}
		return entryObject(entry), nil
	}
	return nil, nil
}

func entryObject(value any) map[string]any {
	if value == nil {
		return nil
	}
	if entry, ok := value.(map[string]any); ok {
		return entry
	}
	// A nonobject is still an existing entry requiring a replacement preview.
	return map[string]any{"_invalid_entry": true}
}

func clientEntryMatches(id string, entry map[string]any, endpoint string) bool {
	if entry == nil || entry["url"] != endpoint {
		return false
	}
	key := "http_headers"
	if id == "claude-code" {
		if entry["type"] != "http" || len(entry) != 3 {
			return false
		}
		key = "headers"
	} else if len(entry) != 2 {
		return false
	}
	headers, ok := entry[key].(map[string]any)
	if !ok || len(headers) != 1 {
		return false
	}
	authorization, ok := headers["Authorization"].(string)
	return ok && strings.HasPrefix(authorization, "Bearer ") && len(authorization) > len("Bearer ") && !strings.ContainsAny(authorization, "\r\n\x00")
}

func mergeClientConfig(id string, data []byte, exists bool, endpoint, token string) ([]byte, error) {
	entry, err := clientEntry(id, data, exists)
	if err != nil {
		return nil, err
	}
	if clientEntryMatches(id, entry, endpoint) {
		headersKey := "http_headers"
		if id == "claude-code" {
			headersKey = "headers"
		}
		if entry[headersKey].(map[string]any)["Authorization"] == "Bearer "+token {
			return append([]byte(nil), data...), nil
		}
	}
	if id == "codex" {
		return mergeCodexConfig(data, endpoint, token)
	}
	root := make(map[string]json.RawMessage)
	if exists {
		root, err = parseClientJSON(data)
		if err != nil {
			return nil, err
		}
	}
	servers, err := jsonServerMap(root)
	if err != nil {
		return nil, err
	}
	entry = map[string]any{"type": "http", "url": endpoint, "headers": map[string]any{"Authorization": "Bearer " + token}}
	servers["xmesh"], err = json.Marshal(entry)
	if err != nil {
		return nil, errors.New("encode Claude Code MCP entry")
	}
	root["mcpServers"], err = json.Marshal(servers)
	if err != nil {
		return nil, errors.New("encode Claude Code MCP servers")
	}
	result, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, errors.New("encode Claude Code configuration")
	}
	return append(result, '\n'), nil
}

func parseClientJSON(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := uniqueClientJSONValue(decoder); err != nil {
		return nil, errors.New("Claude Code configuration is not valid JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("Claude Code configuration is not valid JSON")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil || result == nil {
		return nil, errors.New("Claude Code configuration must be a JSON object")
	}
	return result, nil
}

func uniqueClientJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("invalid JSON delimiter")
	}
	keys := make(map[string]bool)
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errors.New("duplicate or invalid JSON key")
			}
			keys[name] = true
		}
		if err := uniqueClientJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func jsonServerMap(root map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	servers := make(map[string]json.RawMessage)
	if raw, ok := root["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
			return nil, errors.New("Claude Code mcpServers must be a JSON object")
		}
	}
	return servers, nil
}

type clientTOMLExpression struct {
	start, end int
	path       []string
}

// TOML AST ranges let table-style configurations retain every unrelated byte,
// including comments. An inline mcp_servers ancestor is re-encoded as a whole
// namespace so the other entries retain their parsed values.
func mergeCodexConfig(data []byte, endpoint, token string) ([]byte, error) {
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	var expressions []clientTOMLExpression
	inlineAncestor := false
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Table && node.Kind != unstable.ArrayTable && node.Kind != unstable.KeyValue {
			continue
		}
		var key []string
		iterator := node.Key()
		first := -1
		last := 0
		for iterator.Next() {
			part := iterator.Node()
			key = append(key, string(part.Data))
			if first < 0 {
				first = int(part.Raw.Offset)
			}
			last = int(part.Raw.Offset + part.Raw.Length)
		}
		var path []string
		if node.Kind == unstable.KeyValue {
			path = append(append([]string(nil), table...), key...)
			first = int(node.Raw.Offset)
			last = int(node.Raw.Offset + node.Raw.Length)
			if len(path) == 1 && path[0] == "mcp_servers" {
				inlineAncestor = true
			}
		} else {
			table = key
			path = key
		}
		// A header's key range excludes its brackets; extend to whole lines.
		start := bytes.LastIndexByte(data[:first], '\n') + 1
		end := len(data)
		if next := bytes.IndexByte(data[last:], '\n'); next >= 0 {
			end = last + next + 1
		}
		expressions = append(expressions, clientTOMLExpression{start: start, end: end, path: path})
	}
	if parser.Error() != nil {
		return nil, errors.New("Codex configuration is not valid TOML")
	}
	var suffix []byte
	if inlineAncestor {
		var document map[string]any
		if err := toml.Unmarshal(data, &document); err != nil {
			return nil, errors.New("Codex configuration is not valid TOML")
		}
		servers, ok := document["mcp_servers"].(map[string]any)
		if !ok {
			return nil, errors.New("Codex mcp_servers must be a table")
		}
		servers["xmesh"] = map[string]any{"url": endpoint, "http_headers": map[string]any{"Authorization": "Bearer " + token}}
		var err error
		suffix, err = toml.Marshal(map[string]any{"mcp_servers": servers})
		if err != nil {
			return nil, errors.New("encode Codex MCP servers")
		}
	} else {
		quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
		suffix = []byte("[mcp_servers.xmesh]\nurl = " + quote(endpoint) + "\nhttp_headers = { Authorization = " + quote("Bearer "+token) + " }\n")
	}
	var result bytes.Buffer
	position := 0
	for _, expression := range expressions {
		remove := len(expression.path) >= 2 && expression.path[0] == "mcp_servers" && expression.path[1] == "xmesh"
		if inlineAncestor && len(expression.path) >= 1 && expression.path[0] == "mcp_servers" {
			remove = true
		}
		if !remove {
			continue
		}
		result.Write(data[position:expression.start])
		position = expression.end
	}
	result.Write(data[position:])
	if result.Len() > 0 && !bytes.HasSuffix(result.Bytes(), []byte("\n")) {
		result.WriteByte('\n')
	}
	result.WriteByte('\n')
	result.Write(suffix)
	// Reparse the complete result before any file can be created or replaced.
	if _, err := clientEntry("codex", result.Bytes(), true); err != nil {
		return nil, errors.New("merged Codex configuration could not be validated")
	}
	return result.Bytes(), nil
}
