package main

import "testing"

func TestSetupUnitConfigIdentity(t *testing.T) {
	path := "/home/me/项目 with spaces/xmesh.yaml"
	for _, tc := range []struct {
		name, unit string
		want       bool
	}{
		{"generated", `[Service]
ExecStart=/opt/xmesh\x20bin/xmesh "serve" "--config" "/home/me/项目 with spaces/xmesh.yaml" "--service"`, true},
		{"equals", `[Service]
ExecStart=/opt/xmesh serve "--config=/home/me/项目 with spaces/xmesh.yaml"`, true},
		{"escaped", `[Service]
ExecStart=/opt/xmesh serve --config /home/me/项目\x20with\x20spaces/xmesh.yaml`, true},
		{"suffix", `[Service]
ExecStart=/opt/xmesh serve "--config" "/home/me/项目 with spaces/xmesh.yaml.old"`, false},
		{"comment", `# /home/me/项目 with spaces/xmesh.yaml
[Service]
ExecStart=/opt/xmesh serve --config /other/xmesh.yaml`, false},
		{"duplicate", `[Service]
ExecStart=/opt/xmesh serve --config /other/xmesh.yaml "--config" "/home/me/项目 with spaces/xmesh.yaml"`, false},
		{"terminator", `[Service]
ExecStart=/opt/xmesh serve -- --config "/home/me/项目 with spaces/xmesh.yaml"`, false},
		{"consumed-flag", `[Service]
ExecStart=/opt/xmesh serve --state-dir --config "/home/me/项目 with spaces/xmesh.yaml"`, false},
		{"dotdot-alias", `[Service]
ExecStart=/opt/xmesh serve --config "/home/me/项目 with spaces/link/../xmesh.yaml"`, false},
		{"wrong-section", `[Unit]
ExecStart=/opt/xmesh serve --config "/home/me/项目 with spaces/xmesh.yaml"`, false},
		{"multiple", `[Service]
ExecStart=/opt/xmesh serve --config "/home/me/项目 with spaces/xmesh.yaml"
ExecStart=/other/tool`, false},
		{"variable", `[Service]
ExecStart=/opt/xmesh serve --config "$HOME/项目 with spaces/xmesh.yaml"`, false},
		{"specifier", `[Service]
ExecStart=/opt/xmesh serve --config "%h/项目 with spaces/xmesh.yaml"`, false},
		{"unterminated", `[Service]
ExecStart=/opt/xmesh serve --config "/home/me/项目 with spaces/xmesh.yaml`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := setupUnitConfigMatches([]byte(tc.unit), path); got != tc.want {
				t.Fatalf("matched=%v want %v", got, tc.want)
			}
		})
	}
}

func TestSetupUnitManagerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         bool
	}{
		{"loaded-base", "FragmentPath=/home/me/xmesh.service\nDropInPaths=\nNeedDaemonReload=no\n", true},
		{"override", "FragmentPath=/home/me/xmesh.service\nDropInPaths=/home/me/xmesh.service.d/override.conf\nNeedDaemonReload=no\n", false},
		{"stale", "FragmentPath=/home/me/xmesh.service\nDropInPaths=\nNeedDaemonReload=yes\n", false},
		{"foreign", "FragmentPath=/other/xmesh.service\nDropInPaths=\nNeedDaemonReload=no\n", false},
		{"incomplete", "FragmentPath=/home/me/xmesh.service\nNeedDaemonReload=no\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if setupUnitManagerMatches([]byte(tc.output), "/home/me/xmesh.service") != tc.want {
				t.Fatal("wrong manager identity verdict")
			}
		})
	}
}
