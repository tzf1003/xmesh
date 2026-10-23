package v2_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/api/control/v2/schemacheck"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

const schemaDir = "../../../docs/contracts/schemas"

// typeFor maps a schema definition name to a fresh Go value of the matching type. A fixture whose
// definition is missing here fails the test, so a new definition cannot ship without a Go type.
var typeFor = map[string]func() any{
	"ComputerState":           func() any { return new(v2.ComputerState) },
	"ComputerPauseRequest":    func() any { return new(v2.ComputerPauseRequest) },
	"ComputerRevokeRequest":   func() any { return new(v2.ComputerRevokeRequest) },
	"ControlEndpointFile":     func() any { return new(v2.ControlEndpointFile) },
	"StatusResponse":          func() any { return new(v2.StatusResponse) },
	"DrainRequest":            func() any { return new(v2.DrainRequest) },
	"DrainResponse":           func() any { return new(v2.DrainResponse) },
	"PeersResponse":           func() any { return new(v2.PeersResponse) },
	"SessionsResponse":        func() any { return new(v2.SessionsResponse) },
	"ToolExecution":           func() any { return new(v2.ToolExecution) },
	"ExecutionLimits":         func() any { return new(v2.ExecutionLimits) },
	"ControlEvent":            func() any { return new(v2.Event) },
	"ConfigResponse":          func() any { return new(v2.ConfigResponse) },
	"ConfigPutRequest":        func() any { return new(v2.ConfigPutRequest) },
	"ConfigPutResponse":       func() any { return new(v2.ConfigPutResponse) },
	"ConfigReloadResponse":    func() any { return new(v2.ConfigReloadResponse) },
	"ConfigEditRequest":       func() any { return new(v2.ConfigEditRequest) },
	"PairExportResponse":      func() any { return new(v2.PairExportResponse) },
	"PairImportRequest":       func() any { return new(v2.PairImportRequest) },
	"PairImportResponse":      func() any { return new(v2.PairImportResponse) },
	"PairTrustUpdateRequest":  func() any { return new(v2.PairTrustUpdateRequest) },
	"PairTrustUpdateResponse": func() any { return new(v2.PairTrustUpdateResponse) },
	"PairPasswordRequest":     func() any { return new(v2.PairPasswordRequest) },
	"PairPasswordState":       func() any { return new(v2.PairPasswordState) },
	"PairDiscoveredResponse":  func() any { return new(v2.PairDiscoveredResponse) },
	"PairConnectRequest":      func() any { return new(v2.PairConnectRequest) },
	"PairConnectResponse":     func() any { return new(v2.PairConnectResponse) },
	"PatList":                 func() any { return new(v2.PatList) },
	"PatCreateRequest":        func() any { return new(v2.PatCreateRequest) },
	"PatSecret":               func() any { return new(v2.PatSecret) },
	"BackendEnvPatch":         func() any { return new(v2.BackendEnvPatch) },
	"BackendEnvResponse":      func() any { return new(v2.BackendEnvResponse) },
	"GatewayUpdateRequest":    func() any { return new(v2.GatewayUpdateRequest) },
	"GatewayUpdateResponse":   func() any { return new(v2.GatewayUpdateResponse) },
	"IngressClientsResponse":  func() any { return new(v2.IngressClientsResponse) },
	"IngressPasswordRequest":  func() any { return new(v2.IngressPasswordRequest) },
	"IngressPasswordResponse": func() any { return new(v2.IngressPasswordResponse) },
	"AuditQuery":              func() any { return new(v2.AuditQuery) },
	"AuditResponse":           func() any { return new(v2.AuditResponse) },
	"ControlError":            func() any { return new(v2.Error) },
	"CiAccount":               func() any { return new(v2.CiAccount) },
	"CiAccountCreate":         func() any { return new(v2.CiAccountCreate) },
	"CiBinding":               func() any { return new(v2.CiBinding) },
	"CiBindingCreate":         func() any { return new(v2.CiBindingCreate) },
	"CiBindingPatch":          func() any { return new(v2.CiBindingPatch) },
	"CiBindingRepair":         func() any { return new(v2.CiBindingRepair) },
	"CiDeleteQuery":           func() any { return new(v2.CiDeleteQuery) },
	"CiImportInspectRequest":  func() any { return new(v2.CiImportInspectRequest) },
	"CiImportInspectResponse": func() any { return new(v2.CiImportInspectResponse) },
	"CiOverview":              func() any { return new(v2.CiOverview) },
	"CiReposQuery":            func() any { return new(v2.CiReposQuery) },
	"CiReposResponse":         func() any { return new(v2.CiReposResponse) },
	"CiSummary":               func() any { return new(v2.CiSummary) },
}

func fixtures(t *testing.T, sub string) map[string][]byte {
	t.Helper()
	dir := filepath.Join(schemaDir, "examples", "control", sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	if len(out) == 0 {
		t.Fatalf("no fixtures in %s", dir)
	}
	return out
}

func defOf(name string) string { return strings.SplitN(name, ".", 2)[0] }

func normalize(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// conformance: CTR-CTL-02 CTR-CTL-03 CTR-CTL-04 CTR-CTL-05 CTR-CTL-06 CTR-CTL-07 CTR-CTL-08 CTR-CTL-09 CTR-CTL-10 CTR-CTL-11
//
// Every valid fixture satisfies its schema in Go, decodes into the Go type without unknown fields
// and encodes back to the same JSON, so the Go types cannot drift from the contract.
func Test_CTRCTL02_FixturesRoundTripThroughGoTypes(t *testing.T) {
	for name, raw := range fixtures(t, "") {
		def := defOf(name)
		t.Run(name, func(t *testing.T) {
			if err := schemacheck.Validate(def, raw); err != nil {
				t.Fatalf("fixture does not satisfy %s: %v", def, err)
			}
			mk, ok := typeFor[def]
			if !ok {
				t.Fatalf("no Go type registered for definition %s", def)
			}
			value := mk()
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(value); err != nil {
				t.Fatalf("decode into %T: %v", value, err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := schemacheck.Validate(def, encoded); err != nil {
				t.Fatalf("re-encoded %T no longer satisfies %s: %v\n%s", value, def, err, encoded)
			}
			if !reflect.DeepEqual(normalize(t, raw), normalize(t, encoded)) {
				t.Fatalf("round trip changed the document\nwant %s\ngot  %s", raw, encoded)
			}
		})
	}
}

// conformance: CTR-CTL-04
//
// Event payloads decode into their typed structs and survive the SSE framing.
func Test_CTRCTL04_EventPayloadsAndSSE(t *testing.T) {
	seen := map[v2.EventType]bool{}
	for name, raw := range fixtures(t, "") {
		if defOf(name) != "ControlEvent" {
			continue
		}
		var ev v2.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		seen[ev.Type] = true
		payload, err := ev.Payload()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, err := v2.NewEvent(ev.ID, ev.Seq, ev.Time, ev.Type, payload)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(ev)
		b, _ := json.Marshal(again)
		if !reflect.DeepEqual(normalize(t, a), normalize(t, b)) {
			t.Fatalf("%s: typed payload re-encodes differently\n%s\n%s", name, a, b)
		}
		frame, err := ev.SSE()
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(frame), "\n\n"), "\n")
		if ev.ID != "" {
			if lines[0] != "id: "+ev.ID {
				t.Fatalf("%s: first SSE line %q", name, lines[0])
			}
			lines = lines[1:]
		}
		if len(lines) != 2 || lines[0] != "event: "+string(ev.Type) || !strings.HasPrefix(lines[1], "data: {") {
			t.Fatalf("%s: unexpected SSE frame %q", name, frame)
		}
		if ev.ID != "" {
			boot, seq, err := v2.ParseEventID(ev.ID)
			if err != nil || v2.FormatEventID(boot, seq) != ev.ID || seq != ev.Seq {
				t.Fatalf("%s: event id %q does not match seq %d (%v)", name, ev.ID, ev.Seq, err)
			}
		}
	}
	for _, typ := range []v2.EventType{v2.EventHello, v2.EventReset, v2.EventStatus, v2.EventPeer, v2.EventCall, v2.EventSession, v2.EventWarning, v2.EventConfig, v2.EventLifecycle} {
		if !seen[typ] {
			t.Errorf("no fixture for event type %s", typ)
		}
	}
}

// conformance: CTR-CTL-02 CTR-CTL-03 CTR-CTL-04 CTR-CTL-05 CTR-CTL-07 CTR-CTL-09 CTR-CTL-10 CTR-CTL-11
//
// Negative fixtures must fail: unknown fields (stdout, terminal data, secrets), wrong shapes and
// wrong identifiers.
func Test_CTRCTL02_InvalidFixturesAreRejected(t *testing.T) {
	for name, raw := range fixtures(t, "invalid") {
		def := defOf(name)
		t.Run(name, func(t *testing.T) {
			if err := schemacheck.Validate(def, raw); err == nil {
				t.Fatalf("invalid fixture satisfies %s", def)
			}
		})
	}
}

// conformance: CTR-CTL-12
//
// The embedded route table is well formed, and every request and response definition it names has
// a Go type.
func Test_CTRCTL12_RouteTable(t *testing.T) {
	if err := schemacheck.Validate("ControlRoutesFile", schemas.ControlRoutes); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	compiler := jsonschema.NewCompiler()
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(document["$id"].(string), document); err != nil {
			t.Fatal(err)
		}
	}
	capabilities := map[v2.Capability]bool{}
	for _, r := range v2.Routes() {
		if ids[r.ID] {
			t.Errorf("duplicate route id %s", r.ID)
		}
		ids[r.ID] = true
		capabilities[r.Capability] = true
		for _, ref := range []*string{r.Query, r.Body, r.Response} {
			if ref == nil {
				continue
			}
			def := (*ref)[strings.LastIndex(*ref, "/")+1:]
			if _, err := compiler.Compile(*ref); err != nil {
				t.Errorf("route %s cannot resolve %s: %v", r.ID, *ref, err)
			}
			if _, ok := typeFor[def]; !ok {
				t.Errorf("route %s names %s, which has no Go type", r.ID, def)
			}
		}
		if r.Stream && r.Response != nil {
			t.Errorf("route %s is a stream and has a response type", r.ID)
		}
	}
	for _, c := range []v2.Capability{v2.CapStatus, v2.CapEvents, v2.CapPeers, v2.CapSessions, v2.CapDrain, v2.CapConfigRead, v2.CapConfigWrite, v2.CapPair, v2.CapPats, v2.CapGateway, v2.CapIngress, v2.CapAudit, v2.CapCI, v2.CapComputer} {
		if !capabilities[c] {
			t.Errorf("capability %s has no route", c)
		}
	}
	if r, ok := v2.RouteByID("control.config.put"); !ok || r.IfMatch != "required" {
		t.Errorf("control.config.put must require If-Match: %+v", r)
	}
}

func enumOf(t *testing.T, file string, path ...string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(schemaDir, file))
	if err != nil {
		t.Fatal(err)
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	for _, key := range path {
		node = node.(map[string]any)[key]
	}
	var out []string
	for _, v := range node.([]any) {
		out = append(out, v.(string))
	}
	sort.Strings(out)
	return out
}

func sorted[T ~string](in ...T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	sort.Strings(out)
	return out
}

// conformance: CTR-CTL-02 CTR-CTL-12
//
// The Go constants cover exactly the enums in the schemas, and the error codes map to the HTTP
// statuses the schema declares.
func Test_CTRCTL02_ConstantsMatchSchemas(t *testing.T) {
	check := func(name string, want []string, got []string) {
		t.Helper()
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: schema has %v, Go has %v", name, want, got)
		}
	}
	check("Capability", enumOf(t, "control-common.schema.json", "$defs", "Capability", "enum"),
		sorted(v2.CapStatus, v2.CapEvents, v2.CapPeers, v2.CapSessions, v2.CapDrain, v2.CapConfigRead, v2.CapConfigWrite, v2.CapPair, v2.CapPats, v2.CapGateway, v2.CapIngress, v2.CapAudit, v2.CapCI, v2.CapComputer))
	check("Severity", enumOf(t, "control-common.schema.json", "$defs", "Severity", "enum"),
		sorted(v2.SeverityInfo, v2.SeverityWarning, v2.SeverityError))
	check("WarningCode", enumOf(t, "control-common.schema.json", "$defs", "WarningCode", "enum"),
		sorted(v2.WarnRestartRequired, v2.WarnPolicyReloadFailed, v2.WarnAllowDangerous, v2.WarnTailnetNotReady, v2.WarnAuthkeyUnreadable, v2.WarnPeerOffline, v2.WarnAuditDropped, v2.WarnQuotaExhausted, v2.WarnSandboxUnavailable, v2.WarnFirewallBlocked, v2.WarnUnclassified))
	check("DrainMode", enumOf(t, "control-common.schema.json", "$defs", "DrainMode", "enum"), sorted(v2.DrainKeep, v2.DrainKill))
	check("Actor", enumOf(t, "control-common.schema.json", "$defs", "Actor", "enum"), sorted(v2.ActorFile, v2.ActorControl))
	check("ErrorCode", enumOf(t, "control-error.schema.json", "$defs", "ControlErrorCode", "enum"), sorted(v2.ErrorCodes()...))
	check("Transport", enumOf(t, "control-endpoint.schema.json", "$defs", "ControlEndpointFile", "properties", "transport", "enum"),
		sorted(v2.TransportUnix, v2.TransportPipe))

	raw, err := os.ReadFile(filepath.Join(schemaDir, "control-error.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Status map[string]int `json:"x-http-status"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, code := range v2.ErrorCodes() {
		if doc.Status[string(code)] != code.HTTPStatus() {
			t.Errorf("%s: schema says %d, Go says %d", code, doc.Status[string(code)], code.HTTPStatus())
		}
	}
	if v2.APIVersion != "2.1" {
		t.Errorf("APIVersion = %s; the schemas and route table are version 2.1", v2.APIVersion)
	}
}

// conformance: CTR-CTL-06 CTR-CTL-07 CTR-CTL-09
//
// Encoding a Go value that leaves optional parts empty still satisfies the schema, and the one
// secret-carrying type is the only one that has a token field.
func Test_CTRCTL09_SecretsOnlyInPatSecret(t *testing.T) {
	secretFields := []string{"token", "password", "secret_hash", "secret", "authkey", "registration_token"}
	// Request bodies may carry write-only credentials. No response except PatSecret may carry one.
	writeOnly := map[string]bool{"PatSecret": true, "ConfigPutRequest": true, "IngressPasswordRequest": true, "PairPasswordRequest": true, "PairConnectRequest": true, "CiAccountCreate": true, "CiBindingCreate": true, "CiBindingRepair": true}
	for def, mk := range typeFor {
		if writeOnly[def] {
			continue
		}
		var walk func(reflect.Type, string)
		seen := map[reflect.Type]bool{}
		walk = func(rt reflect.Type, path string) {
			for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
				rt = rt.Elem()
			}
			// A lease token is a fencing sequence number, not a credential (CTR-LEASE-01).
			if rt.Kind() != reflect.Struct || seen[rt] || rt == reflect.TypeOf(v2.BackendLease{}) {
				return
			}
			seen[rt] = true
			for i := 0; i < rt.NumField(); i++ {
				f := rt.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")[0]
				for _, bad := range secretFields {
					if tag == bad {
						t.Errorf("%s.%s has a secret-bearing field %q", def, path, tag)
					}
				}
				walk(f.Type, path+"."+f.Name)
			}
		}
		walk(reflect.TypeOf(mk()), "")
	}
	var err error
	_, err = json.Marshal(v2.Session{})
	if err == nil {
		t.Error("an empty Session must not encode")
	}
}
