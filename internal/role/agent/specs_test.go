package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func Test_ToolSchemasRejectUnknownArgs(t *testing.T) {
	for name, s := range builtinSpecs() {
		if s.schema["additionalProperties"] != false {
			t.Fatal("schema not closed", name)
		}
		args := map[string]any{}
		properties := s.schema["properties"].(map[string]any)
		required, _ := s.schema["required"].([]string)
		for _, key := range required {
			if name == "computer.act" && key == "actions" {
				args[key] = []map[string]any{{"type": "wait", "duration_ms": 1}}
				continue
			}
			switch properties[key].(map[string]any)["type"] {
			case "integer":
				args[key] = 1
			case "boolean":
				args[key] = true
			default:
				args[key] = "x"
			}
		}
		valid, _ := json.Marshal(args)
		if e := s.validate(valid); e != nil {
			t.Fatal("valid arguments rejected", name, e)
		}
		args["bogus_argument"] = "x"
		invalid, _ := json.Marshal(args)
		if e := s.validate(invalid); e == nil {
			t.Fatal("unknown argument accepted", name)
		}
		for _, key := range []string{"caller_id", "owner", "node_id"} {
			if _, advertised := properties[key]; advertised {
				t.Fatal("identity field advertised", name, key)
			}
			delete(args, "bogus_argument")
			args[key] = "forged/owner"
			forged, _ := json.Marshal(args)
			if e := s.validate(forged); e != nil {
				t.Fatal("forged identity must be ignored, not rejected", name, key, e)
			}
			delete(args, key)
		}
	}
}

func Test_ToolSchemasRejectUnknownArgsOverMCP(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	list, e := client.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, listed := range list.Tools {
		schema, ok := listed.InputSchema.(map[string]any)
		if !ok || schema["additionalProperties"] != false {
			t.Fatal("advertised schema is open", listed.Name, listed.InputSchema)
		}
	}
	o := tool(t, client, "fs.stat", map[string]any{"path": "missing.txt", "recurse": true})
	if !o.IsError || object(t, o)["error"] != "invalid_args" {
		t.Fatal("misspelled optional argument accepted", o)
	}
	if o = tool(t, client, "fs.stat", map[string]any{"path": "missing.txt", "owner": "forged/owner"}); o.IsError {
		t.Fatal("forged identity must be ignored", object(t, o))
	}
}
