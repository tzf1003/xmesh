package meshadmin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// providerScenario is the acceptance scenario both control planes must pass with
// the same observable results (design 13, "Provider 一致"): apply a permission
// template through preview/validate/version-check, change a tag, create an auth
// key, lose a write race, and roll back.
func providerScenario(t *testing.T, p Provider, owner string, deviceID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := &MemStore{}

	caps, err := p.Capabilities(ctx)
	if err != nil || !caps.PolicyWrite {
		t.Fatalf("capabilities: %v %+v", err, caps)
	}
	// A control plane with no policy yet has nothing to roll back to: seed one.
	if base, err := p.GetPolicy(ctx); err != nil || !base.Exists {
		if _, err := p.SetPolicy(ctx, "// base policy\n{\"groups\": {}}\n", ""); err != nil {
			t.Fatalf("seed policy: %v", err)
		}
	}

	// 1. template: preview, then apply.
	frag, err := BuildTemplate(TemplatePersonal, TemplateOptions{Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanPolicy(ctx, p, "template personal", func(cur string) (string, []string, error) {
		s, w, err := ApplyFragment(cur, frag, SectionTagOwners, SectionACLs)
		return s, w, err
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.Changed || !strings.Contains(plan.Diff, "+") || !strings.Contains(plan.After, ManagedMarker) {
		t.Fatalf("plan not previewable:\n%s", plan.Diff)
	}
	if _, err := plan.Apply(ctx, p, store, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("unconfirmed apply: %v", err)
	}
	first, err := plan.Apply(ctx, p, store, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	cur, err := p.GetPolicy(ctx)
	if err != nil || !strings.Contains(cur.Text, ManagedMarker) || cur.Version == plan.BaseVersion {
		t.Fatalf("policy not written: %v\n%s", err, cur.Text)
	}
	secs, _ := ManagedSections(cur.Text)
	if len(secs) != 2 {
		t.Fatalf("managed sections: %v", secs)
	}

	// 2. re-planning the same template is a no-op.
	again, err := PlanPolicy(ctx, p, "again", func(c string) (string, []string, error) {
		s, w, err := ApplyFragment(c, frag, SectionTagOwners, SectionACLs)
		return s, w, err
	})
	if err != nil || again.Changed {
		t.Fatalf("second plan should be a no-op: %v changed=%v\n%s", err, again.Changed, again.Diff)
	}

	// 3. tag change.
	d, err := p.SetTags(ctx, deviceID, []string{TagPeer, TagDevice})
	if err != nil {
		t.Fatalf("set tags: %v", err)
	}
	if strings.Join(d.Tags, ",") != "tag:device,tag:peer" && strings.Join(d.Tags, ",") != "tag:peer,tag:device" {
		t.Fatalf("tags after set: %v", d.Tags)
	}

	// 4. auth key.
	k, err := p.CreateAuthKey(ctx, AuthKeyRequest{Tags: []string{TagDevice}, TTL: time.Hour})
	if err != nil || k.Secret == "" || !k.Expires.After(time.Now()) {
		t.Fatalf("auth key: %v %+v", err, k)
	}

	// 5. a stale plan loses the race.
	stale, err := PlanPolicy(ctx, p, "stale", func(c string) (string, []string, error) {
		return strings.Replace(c, ManagedMarker+" acl", "xmesh:managed acl", 1) + "\n// stale\n", nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := PlanPolicy(ctx, p, "winner", func(c string) (string, []string, error) { return c + "\n// winner\n", nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Apply(ctx, p, store, true); err != nil {
		t.Fatalf("winner apply: %v", err)
	}
	if _, err := stale.Apply(ctx, p, store, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale apply: want ErrConflict, got %v", err)
	}

	// 6. rollback restores the pre-template policy exactly.
	rb, err := Rollback(ctx, p, store, first.SnapshotID)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !rb.Changed {
		t.Fatal("rollback reported no change")
	}
	after, err := p.GetPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after.Text, ManagedMarker) || strings.Contains(after.Text, "winner") {
		t.Fatalf("rollback left changes behind:\n%s", after.Text)
	}
	if strings.TrimSpace(after.Text) != strings.TrimSpace(plan.Before) {
		t.Fatalf("rollback text differs.\n--- want\n%s\n--- got\n%s", plan.Before, after.Text)
	}
	snaps, _ := store.List()
	if len(snaps) < 3 {
		t.Fatalf("expected snapshots for each write, got %d", len(snaps))
	}
}

// conformance: CON-MESH-03 CTR-MESH-02 CTR-MESH-03 RUL-20
func TestScenarioTailscaleFake(t *testing.T) {
	f := newFakeTailscale(t)
	providerScenario(t, tsProvider(t, f, AccessWrite), "autogroup:admin", "1002")
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

func errorsAs[T any](err error, target *T) bool { return errors.As(err, target) }
