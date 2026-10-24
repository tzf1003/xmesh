package connectivity

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCloudflaredIsRunOverHTTP2WithTheTokenInAFile(t *testing.T) {
	args := cloudflaredArgs("/state/token")
	want := "tunnel --no-autoupdate --protocol http2 run --token-file /state/token"
	if got := strings.Join(args, " "); got != want {
		t.Fatalf("%s", got)
	}
}

func TestManagedCloudflaredStartsAndStopsWithTheProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "cloudflared")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { (&Node{}).runCloudflared(ctx, bin, "/state/token"); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(log)
		if strings.Contains(string(raw), "--protocol http2 run --token-file /state/token") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cloudflared not started: %q", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cloudflared outlived its provider")
	}
}
