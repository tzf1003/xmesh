package lease

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// INV-01: uint64 exhaustion is an unrecoverable error that refuses new leases.
func Test_INV01_CounterExhaustionRefusesNewLeases(t *testing.T) {
	m := New()
	defer m.Close()
	m.mu.Lock()
	m.resources["backend:ida"] = &resource{counter: math.MaxUint64 - 1}
	m.mu.Unlock()
	last, err := m.Acquire("backend:ida", "n/a", time.Minute, time.Hour)
	if err != nil || last.Token != math.MaxUint64 {
		t.Fatal(last, err)
	}
	if err = m.Release("backend:ida", "n/a", last.Token); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"n/a", "n/b"} {
		_, err = m.Acquire("backend:ida", owner, time.Minute, time.Hour)
		if e, ok := err.(*toolerr.Error); !ok || e.Code != "internal" {
			t.Fatalf("exhausted counter handed out a lease to %s: %v", owner, err)
		}
	}
	if m.Owner("backend:ida") != "" {
		t.Fatal("exhausted resource has an owner")
	}
}

// INV-01 / CON-CONC-07: expiry, release, backend crash (Invalidate) and drain
// (ReleaseAll) all kill the old token for good, and each new token is larger.
func Test_INV01_EveryRevocationPathKillsOldToken(t *testing.T) {
	m := New()
	defer m.Close()
	now := time.Now()
	m.now = func() time.Time { return now }
	revoke := map[string]func(Lease){
		"expiry":      func(Lease) { now = now.Add(time.Hour) },
		"release":     func(l Lease) { _ = m.Release(l.Resource, l.Owner, l.Token) },
		"crash":       func(l Lease) { _ = m.Invalidate(l.Resource) },
		"release-all": func(Lease) { m.ReleaseAll() },
	}
	var previous uint64
	for _, name := range []string{"expiry", "release", "crash", "release-all"} {
		l, err := m.Acquire("backend:ida", "n/a", time.Minute, time.Hour)
		if err != nil {
			t.Fatal(name, err)
		}
		if l.Token <= previous {
			t.Fatalf("%s: token %d not above %d", name, l.Token, previous)
		}
		previous = l.Token
		revoke[name](l)
		for _, owner := range []string{"n/a", "n/b"} {
			if err = m.Validate(l.Resource, owner, l.Token); err == nil {
				t.Fatalf("%s: old token accepted for %s", name, owner)
			}
		}
	}
}

// CON-CONC-08: a new process (Manager) does not honour tokens issued by the
// previous one, and the first acquire succeeds.
func Test_CONCONC08_NewManagerDoesNotRecognizeOldTokens(t *testing.T) {
	before := New()
	defer before.Close()
	var old Lease
	for range 3 {
		l, err := before.Acquire("backend:ida", "n/a", time.Minute, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		old = l
		if err = before.Release(l.Resource, l.Owner, l.Token); err != nil {
			t.Fatal(err)
		}
	}
	if old.Token != 3 {
		t.Fatal("expected a token above 1 before restart", old.Token)
	}
	after := New() // "restart"
	defer after.Close()
	if err := after.Validate("backend:ida", "n/a", old.Token); err == nil {
		t.Fatal("restarted manager recognised a pre-restart token")
	}
	fresh, err := after.Acquire("backend:ida", "n/b", time.Minute, time.Hour)
	if err != nil || fresh.Token != 1 {
		t.Fatal("first acquire after restart", fresh, err)
	}
	if err = after.Validate("backend:ida", "n/a", old.Token); err == nil {
		t.Fatal("pre-restart token accepted after the first acquire")
	}
}

// CON-CONC-08: the lease table is a plain in-memory map; the package must not
// be able to write it to disk.
func Test_CONCONC08_LeasePackageHasNoPersistence(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "lease.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range parsed.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		switch path {
		case "os", "io", "io/ioutil", "io/fs", "bufio", "database/sql", "encoding/gob", "path/filepath":
			t.Fatalf("lease.go imports %s; leases must not be persisted", path)
		}
	}
}

// CTR-LEASE-01: the lease object matches the schema; the sweep removes expired
// entries without recycling the token counter.
func Test_CTRLEASE01_ObjectSchemaAndSweep(t *testing.T) {
	b, err := schemas.Files.ReadFile("lease.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("urn:lease", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:lease")
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	defer m.Close()
	now := time.Now().UTC()
	m.now = func() time.Time { return now }
	l, err := m.Acquire("backend:ida", "node-abc/s7f", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(l)
	var value map[string]any
	_ = json.Unmarshal(encoded, &value)
	if err = schema.Validate(value); err != nil {
		t.Fatalf("lease object violates lease.schema.json: %v (%s)", err, encoded)
	}
	if value["resource"] != "backend:ida" || value["owner"] != "node-abc/s7f" || value["token"] != float64(1) {
		t.Fatal(value)
	}
	if _, err = time.Parse(time.RFC3339, value["expires"].(string)); err != nil || !strings.HasSuffix(value["expires"].(string), "Z") {
		t.Fatalf("expires %v is not RFC3339 UTC", value["expires"])
	}
	bad := map[string]any{"resource": "ida", "owner": "x", "token": 0, "expires": "tomorrow"}
	if schema.Validate(bad) == nil {
		t.Fatal("schema accepts a malformed lease")
	}
	now = now.Add(2 * time.Minute)
	m.Sweep()
	m.mu.Lock()
	swept := m.resources["backend:ida"].lease == nil
	counter := m.resources["backend:ida"].counter
	m.mu.Unlock()
	if !swept || counter != 1 {
		t.Fatalf("sweep: lease removed=%v counter=%d (must be kept)", swept, counter)
	}
	next, err := m.Acquire("backend:ida", "node-abc/s7f", time.Minute, time.Hour)
	if err != nil || next.Token != 2 {
		t.Fatal(next, err)
	}
}
