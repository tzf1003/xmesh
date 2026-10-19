package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func transferCall(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	// The boundary response carries both MCP text and structured content. Allow
	// for race-instrumented JSON processing on the shared CI runners.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	out, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(name, err)
	}
	return out
}

func Test_FileTransferThroughGateway(t *testing.T) {
	g, root := fixture(t, false)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	s := connect(t, server.URL+"/mcp")
	// Exercise multiple chunks through HTTP MCP and the proxy layers.
	body := bytes.Repeat([]byte{0, 0xff, 0x80, '\r', '\n'}, (3<<20)/5)
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	id := ""
	for offset := 0; offset < len(body); {
		end := min(len(body), offset+(1<<20))
		args := map[string]any{"path": "artifact.bin", "data": base64.StdEncoding.EncodeToString(body[offset:end]), "sha256": sum,
			"transfer_id": id, "byte_offset": offset, "final": end == len(body)}
		up := transferCall(t, s, "local/fs.upload", args)
		if up.IsError {
			t.Fatal("upload failed", object(t, up))
		}
		if end < len(body) {
			id = object(t, up)["transfer_id"].(string)
		} else if object(t, up)["sha256"] != sum {
			t.Fatal("upload checksum")
		}
		offset = end
	}
	var decoded []byte
	downloadArgs := map[string]any{"path": "artifact.bin"}
	for {
		down := transferCall(t, s, "local/fs.download", downloadArgs)
		if down.IsError {
			t.Fatal("download failed", object(t, down))
		}
		out := object(t, down)
		chunk, err := base64.StdEncoding.DecodeString(out["data"].(string))
		if err != nil || out["sha256"] != fmt.Sprintf("%x", sha256.Sum256(chunk)) {
			t.Fatal("chunk integrity", err)
		}
		decoded = append(decoded, chunk...)
		if out["eof"] == true {
			if out["file_sha256"] != sum {
				t.Fatal("file integrity")
			}
			break
		}
		downloadArgs["transfer_id"], downloadArgs["byte_offset"] = out["transfer_id"], out["next_offset"]
	}
	if !bytes.Equal(body, decoded) {
		t.Fatal("download bytes")
	}
	args := map[string]any{"path": "artifact.bin", "data": "", "sha256": fmt.Sprintf("%x", sha256.Sum256(nil))}
	refused := transferCall(t, s, "local/fs.upload", args)
	if !refused.IsError || object(t, refused)["error"] != "conflict" {
		t.Fatal("existing file accepted")
	}
	if data, err := os.ReadFile(filepath.Join(root, "artifact.bin")); err != nil || !bytes.Equal(body, data) {
		t.Fatal("conflict changed file", err)
	}
	args["data"], args["sha256"], args["force"] = "", fmt.Sprintf("%x", sha256.Sum256(nil)), true
	if out := transferCall(t, s, "local/fs.upload", args); out.IsError {
		t.Fatal("forced upload failed", object(t, out))
	}
	if data, err := os.ReadFile(filepath.Join(root, "artifact.bin")); err != nil || len(data) != 0 {
		t.Fatal("forced upload bytes", err)
	}
}

func Test_FileTransferDevicePrefix(t *testing.T) {
	g, root := fixture(t, true)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "transfer-client", Scopes: []string{"devices:read", "fs:*"}}, nil
	}))
	t.Cleanup(server.Close)
	s := connect(t, server.URL+"/mcp")
	list, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"test-node/fs.upload", "test-node/fs.download"} {
		found := false
		for _, tool := range list.Tools {
			found = found || tool.Name == name
		}
		if !found {
			t.Fatal("device transfer missing", name)
		}
	}
	body := []byte{0, 1, 0xff, 0xfe, '\r', '\n'}
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	args := map[string]any{"path": "remote.bin", "data": base64.StdEncoding.EncodeToString(body), "sha256": sum}
	if out := call(t, s, "test-node/fs.upload", args); out.IsError {
		t.Fatal(object(t, out))
	}
	out := call(t, s, "test-node/fs.download", map[string]any{"path": "remote.bin"})
	if out.IsError || object(t, out)["sha256"] != sum || object(t, out)["data"] != args["data"] {
		t.Fatal("remote transfer integrity")
	}
	if data, err := os.ReadFile(filepath.Join(root, "remote.bin")); err != nil || !bytes.Equal(data, body) {
		t.Fatal("remote target bytes", err)
	}
}
