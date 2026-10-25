package cfadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeRelease(t *testing.T, name string, body []byte, digest string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"2099.1.0","assets":[{"name":%q,"browser_download_url":%q,"digest":%q}]}`, name, srv.URL+"/dl", digest)
	})
	mux.HandleFunc("/dl", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallCloudflaredVerifiesDigest(t *testing.T) {
	body := []byte("#!/bin/sh\necho cloudflared\n")
	sum := sha256.Sum256(body)
	srv := fakeRelease(t, "cloudflared-linux-amd64", body, "sha256:"+hex.EncodeToString(sum[:]))
	dir := filepath.Join(t.TempDir(), "bin")
	in := CloudflaredInstaller{API: srv.URL + "/release", GOOS: "linux", GOARCH: "amd64"}
	path, err := in.Install(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(body) {
		t.Fatalf("content %q", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", info.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	// A second call keeps the installed file and needs no network.
	if again, err := (CloudflaredInstaller{API: "http://127.0.0.1:1/x", GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), dir); err != nil || again != path {
		t.Fatalf("second install: %v %v", again, err)
	}
}

func TestInstallCloudflaredRejectsBadDigest(t *testing.T) {
	srv := fakeRelease(t, "cloudflared-linux-amd64", []byte("tampered"), "sha256:"+strings.Repeat("0", 64))
	dir := t.TempDir()
	_, err := CloudflaredInstaller{API: srv.URL + "/release", GOOS: "linux", GOARCH: "amd64"}.Install(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left behind: %v", entries)
	}
}

func TestInstallCloudflaredNeedsPublishedDigest(t *testing.T) {
	srv := fakeRelease(t, "cloudflared-linux-amd64", []byte("x"), "")
	if _, err := (CloudflaredInstaller{API: srv.URL + "/release", GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), t.TempDir()); err == nil {
		t.Fatal("a release without a digest must not be installed")
	}
}

func TestInstallCloudflaredUnsupportedPlatform(t *testing.T) {
	if _, err := (CloudflaredInstaller{GOOS: "plan9", GOARCH: "amd64"}).Install(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected an unsupported-platform error")
	}
}

func TestInstallCloudflaredRefusesForeignDownloadHost(t *testing.T) {
	body := []byte("x")
	sum := sha256.Sum256(body)
	srv := fakeRelease(t, "cloudflared-linux-amd64", body, "sha256:"+hex.EncodeToString(sum[:]))
	// With the official release API the download must live under the official repository; the fake serves it
	// from elsewhere, so a metadata swap like this is refused.
	in := CloudflaredInstaller{GOOS: "linux", GOARCH: "amd64", HTTP: &http.Client{Transport: rewriteTo(srv.URL)}}
	if _, err := in.Install(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "官方仓库") {
		t.Fatalf("err = %v", err)
	}
}

// rewriteTo sends every request to the fake server, whatever host the installer asked for.
type rewriteTo string

func (r rewriteTo) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := http.NewRequest(req.Method, string(r)+"/release", nil)
	if strings.HasSuffix(req.URL.Path, "/dl") {
		u, _ = http.NewRequest(req.Method, string(r)+"/dl", nil)
	}
	return http.DefaultTransport.RoundTrip(u.WithContext(req.Context()))
}
