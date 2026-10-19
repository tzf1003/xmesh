package agent

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"testing"

	toolsfs "github.com/tzf1003/xmesh/internal/fs"
)

func Test_FileTransferErrorsAndPolicy(t *testing.T) {
	_, url, root := conformanceAgent(t, func(agent map[string]any, root string) {
		agent["cwd_policy"] = map[string]any{"allow": []string{root}, "deny": []string{filepath.Join(root, "secret")}}
	})
	client := connect(t, url)
	schema := errorSchema(t)
	args := map[string]any{"path": "artifact", "data": "eA==", "sha256": toolsfs.Hash([]byte("x"))}
	if out := tool(t, client, "fs.upload", args); out.IsError {
		t.Fatal(object(t, out))
	}
	requireEnvelope(t, schema, "existing upload", "conflict", tool(t, client, "fs.upload", args))
	args["force"], args["sha256"] = true, toolsfs.Hash(nil)
	requireEnvelope(t, schema, "checksum failure", "invalid_args", tool(t, client, "fs.upload", args))
	if got, err := os.ReadFile(filepath.Join(root, "artifact")); err != nil || string(got) != "x" {
		t.Fatal("rejected upload changed artifact", err)
	}
	args["path"], args["sha256"] = "secret/file", toolsfs.Hash([]byte("x"))
	requireEnvelope(t, schema, "denied upload", "invalid_cwd", tool(t, client, "fs.upload", args))
	requireEnvelope(t, schema, "missing download", "invalid_cwd", tool(t, client, "fs.download", map[string]any{"path": "missing"}))
	f, err := os.Create(filepath.Join(root, "huge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1<<30 + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	requireEnvelope(t, schema, "large download", "too_large", tool(t, client, "fs.download", map[string]any{"path": "huge"}))
	args["path"], args["data"] = "oversize", base64.StdEncoding.EncodeToString(make([]byte, 1<<20+1))
	requireEnvelope(t, schema, "large upload", "too_large", tool(t, client, "fs.upload", args))
	// Explicit per-tool grants are enforced at the target Agent as well.
	_, scopedURL, _ := conformanceAgent(t, func(agent map[string]any, root string) {
		agent["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"fs.download"}, "backends": []string{}}}
	})
	scoped := connect(t, scopedURL)
	requireEnvelope(t, schema, "upload grant", "unauthorized", tool(t, scoped, "fs.upload", args))
}

func Test_FileTransferDisabledTools(t *testing.T) {
	_, url, _ := conformanceAgent(t, func(agent map[string]any, root string) {
		agent["disabled_tools"] = []string{"fs.upload", "fs.download"}
	})
	client := connect(t, url)
	names := listedToolNames(t, client)
	for _, name := range []string{"fs.upload", "fs.download"} {
		if slices.Contains(names, name) {
			t.Fatal("disabled transfer listed", name)
		}
	}
}

func Test_TransferConflictSchema(t *testing.T) {
	schema := errorSchema(t)
	for _, op := range []string{"fs.upload", "fs.download"} {
		if err := schema.Validate(map[string]any{"error": "conflict", "message": "文件冲突", "path": "file", "op": op}); err != nil {
			t.Fatal("transfer conflict rejected", op, err)
		}
	}
	for _, value := range []map[string]any{
		{"error": "conflict", "message": "文件冲突", "path": "file"},
		{"error": "conflict", "message": "文件冲突", "path": "file", "op": "fs.write"},
		{"error": "conflict", "message": "文件冲突", "op": "fs.upload"},
	} {
		if err := schema.Validate(value); err == nil {
			t.Fatal("incomplete conflict accepted", value)
		}
	}
}
