package control

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
)

func authkeyRequest(f *fixture) (v2.ConfigPutRequest, string) {
	f.t.Helper()
	file := filepath.Join(f.server.o.StateDir, "authkeys", "authkey-12345678-1234-1234-1234-123456789abc")
	doc, err := ApplyEdits([]byte(testConfig), []Edit{{Path: "/schema_version", Value: 2}, {Path: "/mesh/mode", Delete: true}, {Path: "/mesh/lan/enabled", Value: true}, {Path: "/mesh/tailnet", Value: map[string]any{"enabled": true, "control_url": "https://controlplane.tailscale.com", "authkey_file": file}}})
	if err != nil {
		f.t.Fatal(err)
	}
	key := "tskey-auth-control-test"
	return v2.ConfigPutRequest{Content: string(doc), Authkey: &key}, file
}

func TestConfigAuthkeyPreviewAndCommit(t *testing.T) {
	f := newFixture(t)
	req, file := authkeyRequest(f)
	headers := map[string]string{"If-Match": f.config().ETag}
	req.DryRun = true
	preview := f.do("PUT", "/v2/config", req, headers)
	if preview.Status != 200 {
		t.Fatalf("preview: %d %s", preview.Status, preview.Body)
	}
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Fatalf("preview created credential storage: %v", err)
	}
	if f.file() != testConfig {
		t.Fatal("preview changed config")
	}
	req.DryRun = false
	saved := f.do("PUT", "/v2/config", req, headers)
	if saved.Status != 200 {
		t.Fatalf("save: %d %s", saved.Status, saved.Body)
	}
	if err := privdir.Verify(filepath.Dir(file)); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != *req.Authkey+"\n" {
		t.Fatalf("credential: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(file); info.Mode().Perm() != 0600 {
			t.Fatal("credential permissions")
		}
	}
	for _, body := range []string{string(preview.Body), string(saved.Body), f.file(), f.config().Content} {
		if strings.Contains(body, *req.Authkey) {
			t.Fatal("credential value in response or config")
		}
	}
	stale := f.do("PUT", "/v2/config", req, headers)
	apiError(t, stale, v2.ErrPreconditionFailed)
	if raw, _ := os.ReadFile(file); string(raw) != *req.Authkey+"\n" {
		t.Fatal("stale save changed credential")
	}
}

func TestConfigAuthkeyRejectsInvalidInputAndStorage(t *testing.T) {
	f := newFixture(t)
	req, file := authkeyRequest(f)
	if got, err := f.server.configAuthkeyPath([]byte(req.Content), *req.Authkey); err != nil || got != file {
		t.Fatal("valid private credential rejected", err)
	}
	// Validate the private-path boundary independently of YAML's other config rules.
	for _, key := range []string{"", "short", "key with spaces", "tskey-auth-one\ntwo", strings.Repeat("x", 4097)} {
		if _, err := f.server.configAuthkeyPath([]byte(req.Content), key); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	for _, target := range []string{filepath.Join(f.dir, filepath.Base(file)), filepath.Join(filepath.Dir(file), "xmesh.yaml"), filepath.Join(filepath.Dir(file), "..", filepath.Base(file))} {
		content := fmt.Sprintf("schema_version: 2\nmesh:\n  tailnet:\n    enabled: true\n    authkey_file: %q\n", target)
		if _, err := f.server.configAuthkeyPath([]byte(content), *req.Authkey); err == nil {
			t.Fatal("invalid target accepted")
		}
	}
	if err := createConfigAuthkey(file, *req.Authkey); err != nil {
		t.Fatal(err)
	}
	if err := createConfigAuthkey(file, "replacement-key"); err == nil {
		t.Fatal("existing key overwritten")
	}
	if raw, _ := os.ReadFile(file); string(raw) != *req.Authkey+"\n" {
		t.Fatal("existing key changed")
	}
}

func TestConfigAuthkeyRemovesCredentialOnInternalCommitFailure(t *testing.T) {
	f := newFixture(t)
	req, file := authkeyRequest(f)
	lock, err := filelock.Acquire(t.Context(), f.path+".xmesh.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	request := httptest.NewRequest("PUT", "/v2/config", strings.NewReader(fmt.Sprintf(`{"content":%q,"authkey":%q}`, req.Content, *req.Authkey)))
	request.Header.Set("If-Match", f.config().ETag)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.server.putConfig(response, request, nil); close(done) }()
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(file); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("credential not created before commit")
		case <-time.After(time.Millisecond):
		}
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.path, 0700); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("commit did not finish")
	}
	if response.Code != 500 {
		t.Fatalf("expected internal rejection: %d %s", response.Code, response.Body)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("rejected credential retained")
	}
}
