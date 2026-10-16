package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const examples = "../../docs/contracts/schemas/examples/"

func exampleDoc(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(examples + name)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func parseDoc(doc map[string]any) (*Config, error) {
	b, _ := json.Marshal(doc)
	return Parse(b)
}

func clone(doc map[string]any) map[string]any {
	b, _ := json.Marshal(doc)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// conformance: CON-NET-04
func Test_CONNET04_OfficialAndHeadscaleDifferOnlyByControlURL(t *testing.T) {
	for _, name := range []string{"xmesh.config.json", "xmesh.ingress-public.json", "xmesh.ephemeral.json", "xmesh.ingress-multihost.json"} {
		t.Run(name, func(t *testing.T) {
			base := exampleDoc(t, name)
			if base["mesh"].(map[string]any)["mode"] != "tailnet" {
				t.Fatalf("%s is not a tailnet example", name)
			}
			official, selfHosted := clone(base), clone(base)
			official["mesh"].(map[string]any)["control_url"] = "https://controlplane.tailscale.com"
			selfHosted["mesh"].(map[string]any)["control_url"] = "https://headscale.example.net"
			a, err := parseDoc(official)
			if err != nil {
				t.Fatal(err)
			}
			b, err := parseDoc(selfHosted)
			if err != nil {
				t.Fatal(err)
			}
			if a.Mesh.ControlURL == b.Mesh.ControlURL {
				t.Fatal("fixture did not differ")
			}
			// Everything the runtime derives from the config (mesh options, node,
			// agent, gateway, ingress, audit) is the same once the URL is ignored.
			a.Mesh.ControlURL, b.Mesh.ControlURL = "", ""
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("effective configuration differs beyond control_url:\n%+v\n%+v", a, b)
			}
		})
	}
}

// conformance: CON-NET-04
func Test_CONNET04_FunnelOnlyOnOfficialControlPlane(t *testing.T) {
	doc := exampleDoc(t, "xmesh.config.json") // official control plane, ingress.listen = funnel
	if doc["ingress"].(map[string]any)["listen"] != "funnel" {
		t.Fatal("fixture is not a Funnel configuration")
	}
	doc["node"].(map[string]any)["roles"] = []string{"agent", "gateway", "ingress"} // the ingress role activates the check
	if _, err := parseDoc(doc); err != nil {
		t.Fatalf("Funnel on the official control plane rejected: %v", err)
	}
	headscale := clone(doc)
	headscale["mesh"].(map[string]any)["control_url"] = "https://hs.example.com"
	if _, err := parseDoc(headscale); err == nil || !strings.Contains(err.Error(), "Funnel") {
		t.Fatalf("Funnel on Headscale must be a configuration error, got %v", err)
	}
	// Other ingress modes are control-plane agnostic.
	for _, listen := range []string{"tunnel"} {
		other := clone(headscale)
		other["ingress"].(map[string]any)["listen"] = listen
		if _, err := parseDoc(other); err != nil {
			t.Fatalf("ingress %s on Headscale rejected: %v", listen, err)
		}
	}
}

// conformance: CON-NET-04
func Test_CONNET04_NoControlPlaneSpecificBranchesInCode(t *testing.T) {
	var hits []string
	for _, root := range []string{"../../internal", "../../cmd"} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, _ := os.ReadFile(path)
			text := strings.ToLower(string(b))
			// The tailnet administration CLI manages either control plane by design and
			// is not part of the data path (CON-MESH-01), so it is the one exception.
			if strings.Contains(text, "headscale") && filepath.ToSlash(path) != "../../cmd/xmesh/mesh.go" {
				hits = append(hits, path+": headscale")
			}
			if strings.Contains(text, "controlplane.tailscale.com") && !strings.HasSuffix(filepath.ToSlash(path), "internal/config/config.go") {
				hits = append(hits, path+": official control plane host")
			}
			return nil
		})
	}
	if len(hits) != 0 {
		t.Fatalf("control-plane specific code outside the Funnel validation: %v", hits)
	}
}

// conformance: CON-NET-08
func Test_CONNET08_RelayModeRefusesToStart(t *testing.T) {
	invalid := filepath.Join(examples, "invalid", "config-relay.json")
	if _, err := Load(invalid); err == nil {
		t.Fatal("relay accepted")
	}
	// The process entry point loads configuration through NewStore: it must fail
	// for relay, also when relay is mixed into an otherwise valid tailnet config.
	doc := exampleDoc(t, "xmesh.config.json")
	doc["mesh"].(map[string]any)["mode"] = "relay"
	body, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "relay.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("NewStore (used by xmesh serve) accepted mesh.mode=relay")
	}
	// No go-libp2p dependency may be present.
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "libp2p") {
		t.Fatal("go.mod depends on go-libp2p")
	}
}

// conformance: CON-NET-06
func Test_CONNET06_IngressListenModes(t *testing.T) {
	base := exampleDoc(t, "xmesh.ingress-public.json")
	with := func(mutate func(ingress map[string]any)) error {
		doc := clone(base)
		mutate(doc["ingress"].(map[string]any))
		_, err := parseDoc(doc)
		return err
	}
	if err := with(func(map[string]any) {}); err != nil {
		t.Fatalf("public:443 with domain and autocert: %v", err)
	}
	if with(func(i map[string]any) { delete(i, "domain") }) == nil {
		t.Fatal("public:443 without domain accepted")
	}
	if with(func(i map[string]any) { delete(i, "tls") }) == nil {
		t.Fatal("public:443 without tls accepted")
	}
	if with(func(i map[string]any) { i["tls"] = "file" }) == nil {
		t.Fatal("tls: file without certificate files accepted")
	}
	if err := with(func(i map[string]any) { i["tls"], i["cert_file"], i["key_file"] = "file", "/etc/c.pem", "/etc/k.pem" }); err != nil {
		t.Fatalf("tls: file with certificate files: %v", err)
	}
	for _, mode := range []string{"funnel", "tunnel"} {
		if with(func(i map[string]any) { i["listen"] = mode }) == nil {
			t.Fatalf("%s with domain/tls accepted", mode)
		}
		if err := with(func(i map[string]any) { i["listen"] = mode; delete(i, "domain"); delete(i, "tls") }); err != nil && mode == "tunnel" {
			t.Fatalf("tunnel without domain/tls: %v", err)
		}
	}
	if with(func(i map[string]any) { i["listen"] = "0.0.0.0:80" }) == nil {
		t.Fatal("arbitrary listen value accepted")
	}
	// The Agent port is never configurable to a public address: the gateway
	// listener must be loopback.
	for _, listen := range []string{"0.0.0.0:8080", "192.0.2.5:8080", ":8080", "[::1]:8080"} {
		doc := clone(base)
		doc["gateway"].(map[string]any)["listen"] = listen
		if _, err := parseDoc(doc); err == nil {
			t.Fatalf("gateway.listen %q accepted", listen)
		}
	}
}

// conformance: CON-SEC-02 CON-ARCH-07
func Test_CONSEC02_IngressHasNoAuthSwitchOrTenantModel(t *testing.T) {
	base := exampleDoc(t, "xmesh.ingress-public.json")
	for _, field := range []string{"disable_auth", "auth", "anonymous", "insecure", "users", "tenants", "tenant_id", "organizations"} {
		doc := clone(base)
		doc["ingress"].(map[string]any)["oauth"].(map[string]any)[field] = true
		if _, err := parseDoc(doc); err == nil {
			t.Fatalf("ingress.oauth.%s accepted", field)
		}
		doc = clone(base)
		doc["ingress"].(map[string]any)[field] = true
		if _, err := parseDoc(doc); err == nil {
			t.Fatalf("ingress.%s accepted", field)
		}
	}
	// Clients must name at least one scope; an empty list is a configuration
	// error rather than "everything".
	doc := clone(base)
	doc["ingress"].(map[string]any)["oauth"].(map[string]any)["clients"] = map[string]any{"codex": map[string]any{"scopes": []string{}}}
	if _, err := parseDoc(doc); err == nil {
		t.Fatal("client with empty scopes accepted")
	}
	// Issuer must be an https origin.
	doc = clone(base)
	doc["ingress"].(map[string]any)["oauth"].(map[string]any)["issuer"] = "http://mcp.example.com"
	if _, err := parseDoc(doc); err == nil {
		t.Fatal("http issuer accepted")
	}
}

// conformance: CON-SEC-06
func Test_CONSEC06_InvalidPolicyAtFirstStartRefusesStart(t *testing.T) {
	doc := exampleDoc(t, "xmesh.config.json")
	doc["agent"].(map[string]any)["policy"].(map[string]any)["exec"] = map[string]any{"deny_patterns": []string{"("}}
	body, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("first start with an invalid policy was accepted")
	}
}
