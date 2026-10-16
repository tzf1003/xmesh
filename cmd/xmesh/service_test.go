package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
)

func Test_ServiceRuntimeFailurePropagates(t *testing.T) {
	expected := errors.New("listener failed")
	failures := make(chan error, 1)
	p := &program{run: func(context.Context, *config.Store, app.Options) error { return expected }, onFailure: func(e error) { failures <- e }}
	if e := p.Start(nil); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-failures:
		if !errors.Is(e, expected) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime failure did not reach service exit handler")
	}
	for i := 0; i < 2; i++ {
		if e := p.Stop(nil); !errors.Is(e, expected) {
			t.Fatal("stop result", e)
		}
	}
}

func Test_ServiceRequestedStop(t *testing.T) {
	failures := make(chan error, 1)
	p := &program{run: func(ctx context.Context, _ *config.Store, _ app.Options) error { <-ctx.Done(); return nil }, onFailure: func(e error) { failures <- e }}
	if e := p.Start(nil); e != nil {
		t.Fatal(e)
	}
	if e := p.Stop(nil); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-failures:
		t.Fatal("requested stop treated as failure", e)
	default:
	}
}
