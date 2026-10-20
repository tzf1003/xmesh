//go:build !windows && !darwin

package main

import (
	"reflect"
	"testing"
)

func TestSystemdBaselineEnablement(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  []string
	}{
		{"enabled", []string{"--user", "enable", "xmesh-test"}},
		{"enabled-runtime", []string{"--user", "enable", "--runtime", "xmesh-test"}},
		{"disabled", []string{"--user", "disable", "xmesh-test"}},
		{"static", []string{"--user", "disable", "xmesh-test"}},
		{"", nil},
	} {
		if got := systemdRestoreEnablement(tc.state, "xmesh-test"); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("state %s got %v", tc.state, got)
		}
	}
}
