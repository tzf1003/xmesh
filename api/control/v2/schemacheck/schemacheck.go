// Package schemacheck validates JSON documents against the definitions of the control channel
// schemas (docs/contracts/schemas/control-*.schema.json). The kernel's contract tests, the fake
// kernel and this module's own tests use it; production code paths do not need it.
package schemacheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

var (
	once     sync.Once
	initErr  error
	compiler *jsonschema.Compiler
	owner    map[string]string // definition name -> schema $id

	mu       sync.Mutex
	compiled = map[string]*jsonschema.Schema{}
)

func load() error {
	once.Do(func() {
		compiler = jsonschema.NewCompiler()
		compiler.AssertFormat()
		owner = map[string]string{}
		entries, err := schemas.Files.ReadDir(".")
		if err != nil {
			initErr = err
			return
		}
		for _, entry := range entries {
			raw, err := schemas.Files.ReadFile(entry.Name())
			if err != nil {
				initErr = err
				return
			}
			var doc struct {
				ID   string                     `json:"$id"`
				Defs map[string]json.RawMessage `json:"$defs"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				initErr = fmt.Errorf("%s: %w", entry.Name(), err)
				return
			}
			parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				initErr = fmt.Errorf("%s: %w", entry.Name(), err)
				return
			}
			if err := compiler.AddResource(doc.ID, parsed); err != nil {
				initErr = fmt.Errorf("%s: %w", entry.Name(), err)
				return
			}
			if strings.HasPrefix(doc.ID, "urn:xmesh:schema:control-") {
				for name := range doc.Defs {
					owner[name] = doc.ID
				}
			}
		}
	})
	return initErr
}

// Definitions lists the definition names that Validate accepts.
func Definitions() ([]string, error) {
	if err := load(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(owner))
	for name := range owner {
		names = append(names, name)
	}
	return names, nil
}

// Validate checks doc (JSON) against the control definition with the given name, for example
// "StatusResponse" or "ControlEvent". A nil error means the document satisfies the contract.
func Validate(def string, doc []byte) error {
	if err := load(); err != nil {
		return err
	}
	id, ok := owner[def]
	if !ok {
		return fmt.Errorf("schemacheck: no control definition named %q", def)
	}
	mu.Lock()
	schema, ok := compiled[def]
	if !ok {
		var err error
		schema, err = compiler.Compile(id + "#/$defs/" + def)
		if err != nil {
			mu.Unlock()
			return err
		}
		compiled[def] = schema
	}
	mu.Unlock()
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return err
	}
	return schema.Validate(value)
}
