package desktopbroker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// refDesktop is an engine that reports refs. Its reads answer with a fixed title.
type refDesktop struct{ *desktop }

func (refDesktop) Snapshot(context.Context, computer.SnapshotQuery) (computer.Snapshot, error) {
	return computer.Snapshot{}, nil
}
func (refDesktop) RefRead(_ context.Context, e computer.RefReadExecution) (computer.RefReadResult, error) {
	return computer.RefReadResult{Ref: e.Ref, Property: e.Property, Value: json.RawMessage(`"Close"`)}, nil
}
func (refDesktop) RefAct(context.Context, computer.RefExecution) (computer.ActionResult, error) {
	return computer.ActionResult{}, nil
}

// A read crosses the local broker to the engine and its answer comes back intact.
func TestRefReadCrossesTheBrokerToTheEngine(t *testing.T) {
	ctx := context.Background()
	b, err := Start(Options{StateDir: t.TempDir(), Factory: func() (computer.Provider, error) { return refDesktop{&desktop{epoch: "1"}}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c := computer.NewClient(b.Endpoint)
	defer c.Close()
	if _, err = c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := c.RefRead(ctx, computer.RefReadExecution{SnapshotID: "s1", Ref: "@s1:e2", Op: "get", Property: "title"})
	if err != nil || res.Ref != "@s1:e2" || res.Property != "title" || string(res.Value) != `"Close"` {
		t.Fatalf("%+v %v", res, err)
	}
}

// An engine that does not report refs refuses a read with refs_unsupported, as it refuses snapshots.
func TestRefReadIsRefusedByAnEngineWithoutRefs(t *testing.T) {
	ctx := context.Background()
	b, err := Start(Options{StateDir: t.TempDir(), Factory: func() (computer.Provider, error) { return &desktop{epoch: "1"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c := computer.NewClient(b.Endpoint)
	defer c.Close()
	if _, err = c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = c.RefRead(ctx, computer.RefReadExecution{SnapshotID: "s1", Ref: "@s1:e2", Op: "get", Property: "title"})
	if e, ok := err.(*toolerr.Error); !ok || e.Object()["reason"] != "refs_unsupported" {
		t.Fatalf("%v", err)
	}
}
