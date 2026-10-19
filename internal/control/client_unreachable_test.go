package control

import (
	"errors"
	"net/url"
	"strings"
	"syscall"
	"testing"
)

func Test_ControlClientUnreachableIsReadable(t *testing.T) {
	c := &Client{node: "mac-a"}
	raw := &url.Error{Op: "Get", URL: "http://xmesh-control/v2/status", Err: syscall.ECONNREFUSED}
	err := c.unreachable(raw)
	text := err.Error()
	if !strings.Contains(text, "mac-a") || strings.Contains(text, "xmesh-control") || strings.Contains(text, "Get ") {
		t.Fatalf("not readable: %s", text)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("cause lost: %v", err)
	}
}
