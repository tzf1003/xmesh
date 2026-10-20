package main

import (
	"context"
	"errors"
	"github.com/tzf1003/xmesh/internal/onboarding"
	"reflect"
	"testing"
)

func TestSetupLaunchAgentLoadState(t *testing.T) {
	for _, tc := range []struct {
		name, output    string
		err             error
		loaded, running bool
	}{
		{"loaded-idle", `{ "Label" = "xmesh-node"; "LastExitStatus" = 1; }`, nil, true, false},
		{"running", `{ "PID" = 123; }`, nil, true, true},
		{"unloaded", "", errors.New("missing"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, running := setupLaunchAgentState([]byte(tc.output), tc.err)
			if loaded != tc.loaded || running != tc.running {
				t.Fatal(loaded, running)
			}
		})
	}
}
func TestSetupRestoresLoadedAgentWithoutProcess(t *testing.T) {
	for _, tc := range []struct {
		name            string
		loaded, running bool
		want            []string
	}{
		{"idle", true, false, []string{"load"}},
		{"running", true, true, []string{"load", "start"}},
		{"old-journal", false, true, []string{"load", "start"}},
		{"unloaded", false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			err := restoreSetupLaunchAgent(t.Context(), "xmesh-test", onboarding.ServiceSnapshot{Loaded: tc.loaded, Running: tc.running, Path: "/private/xmesh-test.plist"}, func(_ context.Context, args ...string) error { got = append(got, args[0]); return nil })
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatal(got, err)
			}
		})
	}
}
