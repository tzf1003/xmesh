package v1_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/api/control/v1/schemacheck"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

const schemaDir = "../../../docs/contracts/schemas"

// typeFor maps a schema definition name to a fresh Go value of the matching type. A fixture whose
// definition is missing here fails the test, so a new definition cannot ship without a Go type.
var typeFor = map[string]func() any{
	"ControlEndpointFile":     func() any { return new(v1.ControlEndpointFile) },
	"StatusResponse":          func() any { return new(v1.StatusResponse) },
	"DrainRequest":            func() any { return new(v1.DrainRequest) },
	"DrainResponse":           func() any { return new(v1.DrainResponse) },
	"PeersResponse":           func() any { return new(v1.PeersResponse) },
	"SessionsResponse":        func() any { return new(v1.SessionsResponse) },
	"ControlEvent":            func() any { return new(v1.Event) },
	"ConfigResponse":          func() any { return new(v1.ConfigResponse) },
	"ConfigPutRequest":        func() any { return new(v1.ConfigPutRequest) },
	"ConfigPutResponse":       func() any { return new(v1.ConfigPutResponse) },
	"ConfigReloadResponse":    func() any { return new(v1.ConfigReloadResponse) },
	"ConfigEditRequest":       func() any { return new(v1.ConfigEditRequest) },
	"PairExportResponse":      func() any { return new(v1.PairExportResponse) },
	"PairImportRequest":       func() any { return new(v1.PairImportRequest) },
	"PairImportResponse":      func() any { return new(v1.PairImportResponse) },
	"PairPasswordRequest":     func() any { return new(v1.PairPasswordRequest) },
	"PairPasswordState":       func() any { return new(v1.PairPasswordState) },
	"PairDiscoveredResponse":  func() any { return new(v1.PairDiscoveredResponse) },
	"PairConnectRequest":      func() any { return new(v1.PairConnectRequest) },
	"PairConnectResponse":     func() any { return new(v1.PairConnectResponse) },
	"PatList":                 func() any { return new(v1.PatList) },
	"PatCreateRequest":        func() any { return new(v1.PatCreateRequest) },
	"PatSecret":               func() any { return new(v1.PatSecret) },
	"BackendEnvPatch":         func() any { return new(v1.BackendEnvPatch) },
	"BackendEnvResponse":      func() any { return new(v1.BackendEnvResponse) },
	"GatewayUpdateRequest":    func() any { return new(v1.GatewayUpdateRequest) },
	"GatewayUpdateResponse":   func() any { return new(v1.GatewayUpdateResponse) },
	"IngressClientsResponse":  func() any { return new(v1.IngressClientsResponse) },
	"IngressPasswordRequest":  func() any { return new(v1.IngressPasswordRequest) },
	"IngressPasswordResponse": func() any { return new(v1.IngressPasswordResponse) },
	"AuditQuery":              func() any { return new(v1.AuditQuery) },
	"AuditResponse":           func() any { return new(v1.AuditResponse) },
	"ControlError":            func() any { return new(v1.Error) },
	"CiAccount":               func() any { return new(v1.CiAccount) },
	"CiAccountCreate":         func() any { return new(v1.CiAccountCreate) },
	"CiBinding":               func() any { return new(v1.CiBinding) },
	"CiBindingCreate":         func() any { return new(v1.CiBindingCreate) },
	"CiBindingPatch":          func() any { return new(v1.CiBindingPatch) },
	"CiBindingRepair":         func() any { return new(v1.CiBindingRepair) },
	"CiDeleteQuery":           func() any { return new(v1.CiDeleteQuery) },
	"CiImportInspectRequest":  func() any { return new(v1.CiImportInspectRequest) },
	"CiImportInspectResponse": func() any { return new(v1.CiImportInspectResponse) },
	"CiOverview":              func() any { return new(v1.CiOverview) },
	"CiReposQuery":            func() any { return new(v1.CiReposQuery) },
	"CiReposResponse":         func() any { return new(v1.CiReposResponse) },
	"CiSummary":               func() any { return new(v1.CiSummary) },
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
	seen := map[v1.EventType]bool{}
	for name, raw := range fixtures(t, "") {
		if defOf(name) != "ControlEvent" {
			continue
		}
		var ev v1.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		seen[ev.Type] = true
		payload, err := ev.Payload()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, err := v1.NewEvent(ev.ID, ev.Seq, ev.Time, ev.Type, payload)
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
			boot, seq, err := v1.ParseEventID(ev.ID)
			if err != nil || v1.FormatEventID(boot, seq) != ev.ID || seq != ev.Seq {
				t.Fatalf("%s: event id %q does not match seq %d (%v)", name, ev.ID, ev.Seq, err)
			}
		}
	}
	for _, typ := range []v1.EventType{v1.EventHello, v1.EventReset, v1.EventStatus, v1.EventPeer, v1.EventCall, v1.EventSession, v1.EventWarning, v1.EventConfig, v1.EventLifecycle} {
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
	capabilities := map[v1.Capability]bool{}
	for _, r := range v1.Routes() {
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
			if _, ok := typeFor[def]; !ok {
				t.Errorf("route %s names %s, which has no Go type", r.ID, def)
			}
		}
		if r.Stream && r.Response != nil {
			t.Errorf("route %s is a stream and has a response type", r.ID)
		}
	}
	for _, c := range []v1.Capability{v1.CapStatus, v1.CapEvents, v1.CapPeers, v1.CapSessions, v1.CapDrain, v1.CapConfigRead, v1.CapConfigWrite, v1.CapPair, v1.CapPats, v1.CapGateway, v1.CapIngress, v1.CapAudit, v1.CapCI} {
		if !capabilities[c] {
			t.Errorf("capability %s has no route", c)
		}
	}
	if r, ok := v1.RouteByID("control.config.put"); !ok || r.IfMatch != "required" {
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
		sorted(v1.CapStatus, v1.CapEvents, v1.CapPeers, v1.CapSessions, v1.CapDrain, v1.CapConfigRead, v1.CapConfigWrite, v1.CapPair, v1.CapPats, v1.CapGateway, v1.CapIngress, v1.CapAudit, v1.CapCI))
	check("Severity", enumOf(t, "control-common.schema.json", "$defs", "Severity", "enum"),
		sorted(v1.SeverityInfo, v1.SeverityWarning, v1.SeverityError))
	check("WarningCode", enumOf(t, "control-common.schema.json", "$defs", "WarningCode", "enum"),
		sorted(v1.WarnRestartRequired, v1.WarnPolicyReloadFailed, v1.WarnAllowDangerous, v1.WarnTailnetNotReady, v1.WarnAuthkeyUnreadable, v1.WarnPeerOffline, v1.WarnAuditDropped, v1.WarnQuotaExhausted, v1.WarnSandboxUnavailable, v1.WarnFirewallBlocked, v1.WarnUnclassified))
	check("DrainMode", enumOf(t, "control-common.schema.json", "$defs", "DrainMode", "enum"), sorted(v1.DrainKeep, v1.DrainKill))
	check("Actor", enumOf(t, "control-common.schema.json", "$defs", "Actor", "enum"), sorted(v1.ActorFile, v1.ActorControl))
	check("ErrorCode", enumOf(t, "control-error.schema.json", "$defs", "ControlErrorCode", "enum"), sorted(v1.ErrorCodes()...))
	check("Transport", enumOf(t, "control-endpoint.schema.json", "$defs", "ControlEndpointFile", "properties", "transport", "enum"),
		sorted(v1.TransportUnix, v1.TransportPipe))

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
	for _, code := range v1.ErrorCodes() {
		if doc.Status[string(code)] != code.HTTPStatus() {
			t.Errorf("%s: schema says %d, Go says %d", code, doc.Status[string(code)], code.HTTPStatus())
		}
	}
	if v1.APIVersion != "1.8" {
		t.Errorf("APIVersion = %s; the schemas and route table are version 1.8", v1.APIVersion)
	}
}

// conformance: CTR-CTL-06 CTR-CTL-07 CTR-CTL-09
//
// Encoding a Go value that leaves optional parts empty still satisfies the schema, and the one
// secret-carrying type is the only one that has a token field.
func Test_CTRCTL09_SecretsOnlyInPatSecret(t *testing.T) {
	secretFields := []string{"token", "password", "secret_hash", "secret", "authkey", "registration_token"}
	// Request bodies that carry a password are write-only. No response type may carry one.
	writeOnly := map[string]bool{"PatSecret": true, "IngressPasswordRequest": true, "PairPasswordRequest": true, "PairConnectRequest": true, "CiAccountCreate": true, "CiBindingCreate": true, "CiBindingRepair": true}
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
			if rt.Kind() != reflect.Struct || seen[rt] || rt == reflect.TypeOf(v1.BackendLease{}) {
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
	_, err = json.Marshal(v1.Session{})
	if err == nil {
		t.Error("an empty Session must not encode")
	}
}
