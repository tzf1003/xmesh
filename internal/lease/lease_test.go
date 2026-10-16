package lease

import (
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"sync"
	"testing"
	"time"
)

// conformance: INV-01 CON-CONC-07
func Test_INV01_MonotonicOwnerAndExpiry(t *testing.T) {
	m := New()
	defer m.Close()
	now := time.Now()
	m.now = func() time.Time { return now }
	a, e := m.Acquire("backend:ida", "n/a", time.Second, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	retry, _ := m.Acquire("backend:ida", "n/a", time.Second, time.Minute)
	if retry.Token != a.Token {
		t.Fatal("acquire retry changed token")
	}
	if _, e = m.Acquire("backend:ida", "n/b", time.Second, time.Minute); e == nil {
		t.Fatal("concurrent owner")
	}
	if e = m.Validate("backend:ida", "n/b", a.Token); e == nil || e.(*toolerr.Error).Fields["reason"] != "token_mismatch" {
		t.Fatal(e)
	}
	now = now.Add(2 * time.Second)
	if e = m.Validate("backend:ida", "n/a", a.Token); e == nil {
		t.Fatal("expired accepted")
	}
	b, _ := m.Acquire("backend:ida", "n/b", time.Second, time.Minute)
	if b.Token <= a.Token {
		t.Fatal("token reused")
	}
	if e = m.Release("backend:ida", "n/b", b.Token); e != nil {
		t.Fatal(e)
	}
	if e = m.Release("backend:ida", "n/b", b.Token); e != nil {
		t.Fatal("release retry", e)
	}
	c, _ := m.Acquire("backend:ida", "n/a", time.Second, time.Minute)
	_ = m.Invalidate("backend:ida")
	if e = m.Validate("backend:ida", "n/a", c.Token); e == nil {
		t.Fatal("crashed token accepted")
	}
	d, _ := m.Acquire("backend:ida", "n/a", time.Second, time.Minute)
	if d.Token <= c.Token {
		t.Fatal("restart token reused")
	}
}

// conformance: CTR-LEASE-01 INV-01
func Test_CTRLEASE01_Concurrent(t *testing.T) {
	m := New()
	defer m.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	owners := 0
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, e := m.Acquire("backend:ida", string(rune('A'+i))+"/session", time.Second, time.Minute); e == nil {
				mu.Lock()
				owners++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if owners != 1 {
		t.Fatal(owners)
	}
}

// conformance: RUL-06
func Test_RUL06_ExactUint64(t *testing.T) {
	for _, raw := range []string{`"18446744073709551615"`, `18446744073709551615`} {
		v, e := ParseToken(json.RawMessage(raw))
		if e != nil || v != ^uint64(0) {
			t.Fatal(v, e)
		}
	}
	for _, raw := range []string{`-1`, `1.5`, `null`, `"0"`} {
		if _, e := ParseToken(json.RawMessage(raw)); e == nil {
			t.Fatal(raw)
		}
	}
}
