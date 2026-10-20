package main

import "testing"

func TestServiceScopeUsesQueryPermission(t *testing.T) {
	system, err := installedSystemService("EventLog")
	if err != nil || !system {
		t.Fatal("read-only SCM scope query must work for an ordinary user", system, err)
	}
	system, err = installedSystemService("xmesh-nonexistent-scope-test")
	if err != nil || system {
		t.Fatal("missing registration must be distinguishable from access denied", system, err)
	}
}
