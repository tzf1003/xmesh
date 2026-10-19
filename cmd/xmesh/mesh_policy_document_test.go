package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMeshPolicyDocumentInjectedInput(t *testing.T) {
	var out bytes.Buffer
	err := meshRun([]string{"policy", "document", "--json"}, &out, strings.NewReader(`{"text":"{}"}`))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Text    string
		Regions []any
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Text != "{}" || len(doc.Regions) != 0 {
		t.Fatalf("wrong document: %s", out.String())
	}
}
