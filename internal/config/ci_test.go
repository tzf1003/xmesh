package config

import (
	"os"
	"strings"
	"testing"
)

func ciExample(t *testing.T, mutate func(string) string) (*Config, error) {
	t.Helper()
	raw, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ci.json")
	if err != nil {
		t.Fatal(err)
	}
	return Parse([]byte(mutate(string(raw))))
}

// conformance: CTR-CTL-15 CTR-CFG-01
func Test_CTRCTL15_CIConfigDefaultsAndValidation(t *testing.T) {
	c, err := ciExample(t, func(s string) string { return s })
	if err != nil {
		t.Fatal(err)
	}
	if got := c.CI.Bindings[0]; got.Scope != "repo" || got.Source != "account" || got.Concurrency != 2 {
		t.Errorf("binding defaults: %+v", got)
	}
	if got := c.CI.Bindings[1]; got.Concurrency != 1 || got.Source != "import" || got.Dir == "" {
		t.Errorf("import binding: %+v", got)
	}
	if c.CI.Accounts[0].Host != "github.com" {
		t.Errorf("account host default: %+v", c.CI.Accounts[0])
	}
	if !c.CI.PreventSleepWhileBusy || c.CI.StopTimeoutDuration().Minutes() != 30 {
		t.Errorf("sleep/stop defaults: %+v", c.CI)
	}

	cases := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{"duplicate binding id", func(s string) string { return strings.Replace(s, `"id": "legacy"`, `"id": "xmesh"`, 1) }, "/ci/bindings/1/id"},
		{"duplicate repository", func(s string) string { return strings.Replace(s, `"tzf1003/legacy"`, `"TZF1003/XMesh"`, 1) }, "/ci/bindings/1/target"},
		{"unknown account", func(s string) string { return strings.Replace(s, `"account": "gh-tzf1003"`, `"account": "nobody"`, 1) }, ""},
		{"relative directory", func(s string) string {
			return strings.Replace(s, `/Users/xiaosong/actions-runner`, `actions-runner`, 1)
		}, "/ci/bindings/1/dir"},
		{"import without directory", func(s string) string {
			return strings.Replace(s, `"dir": "/Users/xiaosong/actions-runner"`, `"labels": []`, 1)
		}, "/ci/bindings/1/dir"},
		{"bad stop timeout", func(s string) string { return strings.Replace(s, `"stop_timeout": "30m"`, `"stop_timeout": "-5m"`, 1) }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ciExample(t, tc.mutate)
			if err == nil {
				t.Fatal("configuration was accepted")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

func Test_CTRCTL15_CIConfigHasNoSecretFields(t *testing.T) {
	_, err := ciExample(t, func(s string) string {
		return strings.Replace(s, `"account": "gh-tzf1003",`, `"account": "gh-tzf1003", "token": "ghp_secret",`, 1)
	})
	if err == nil {
		t.Fatal("a token in the configuration was accepted")
	}
}
