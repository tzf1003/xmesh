package control

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func Test_DecodeRequiresExactlyOneJSONValue(t *testing.T) {
	for _, raw := range []string{`{"mode":"keep"}}`, `{"mode":"keep"}]`, `{"mode":"keep"}{"mode":"kill"}`, `{"mode":"keep"} true`} {
		var target struct {
			Mode string `json:"mode"`
		}
		req := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		if err := decode(req, &target, true); err == nil {
			t.Fatalf("accepted malformed trailing data: %q", raw)
		}
	}
	var target struct {
		Mode string `json:"mode"`
	}
	if err := decode(httptest.NewRequest("POST", "/", strings.NewReader(`{"mode":"keep"} `)), &target, true); err != nil {
		t.Fatal(err)
	}
}
