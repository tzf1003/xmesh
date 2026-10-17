package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// conformance: CON-NET-08 CON-NET-01
func Test_CONNET08_ServeRejectsRelayAndPublicGatewayBeforeListening(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	relay := write("relay.json", `{"node":{"name":"relay-node","roles":["agent"]},"mesh":{"mode":"relay"},"agent":{"cwd_policy":{"allow":["/tmp"]}}}`)
	public := write("public.json", `{"node":{"name":"public-gw","roles":["agent","gateway"]},"mesh":{"mode":"lan","lan":{"mdns":true}},"gateway":{"listen":"192.0.2.5:8080"},"agent":{"cwd_policy":{"allow":["/tmp"]}}}`)
	for name, path := range map[string]string{"mesh.mode relay": relay, "gateway on a specific address": public} {
		// serve returns the configuration error instead of starting any listener;
		// a process that started would block here.
		done := make(chan error, 1)
		go func() { done <- serve([]string{"--config", path, "--local", "--listen", "127.0.0.1:0"}) }()
		select {
		case err := <-done:
			if err == nil || strings.Contains(err.Error(), "没有可启动") {
				t.Fatalf("%s: serve returned %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: serve did not refuse to start", name)
		}
	}
}
