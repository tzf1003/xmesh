package ci

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testToken = "github_pat_11AAAAAAAAAAAAAAAAAAAAAA"

// githubStub answers the endpoints this package calls, and records the credential it was given.
func githubStub(t *testing.T) (API, *[]string) {
	t.Helper()
	var seen []string
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			_, _ = w.Write([]byte(`{"login":"Octo"}`))
		}
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<https://api.github.com/user/repos?page=2>; rel="next"`)
		}
		_, _ = w.Write([]byte(`[
		 {"full_name":"o/mine","private":true,"permissions":{"admin":true}},
		 {"full_name":"o/readonly","private":false,"permissions":{"admin":false}},
		 {"full_name":"o/old","private":false,"archived":true,"permissions":{"admin":true}},
		 {"full_name":"o/open","private":false,"permissions":{"admin":true}}]`))
	})
	mux.HandleFunc("/repos/o/mine/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"REGTOKEN"}`))
		}
	})
	mux.HandleFunc("/repos/o/mine/actions/runners/remove-token", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"RMTOKEN"}`))
		}
	})
	mux.HandleFunc("/repos/o/other/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return API{APIBase: srv.URL}, &seen
}

// conformance: CTR-CTL-15
func Test_CTRCTL15_GitHubAPI(t *testing.T) {
	api, _ := githubStub(t)
	ctx := context.Background()

	login, err := api.User(ctx, "github.com", testToken)
	if err != nil || login != "Octo" {
		t.Fatalf("User = %q, %v", login, err)
	}
	if _, err = api.User(ctx, "github.com", "wrong"); !errors.Is(err, ErrRejected) {
		t.Fatalf("bad token: %v", err)
	}

	repos, next, err := api.AdminRepos(ctx, "github.com", testToken, "", 1)
	if err != nil || next != 2 || len(repos) != 2 || repos[0].FullName != "o/mine" || !repos[0].Private || repos[1].FullName != "o/open" {
		t.Fatalf("AdminRepos = %+v next=%d %v (only unarchived repositories with admin rights are offered)", repos, next, err)
	}
	if repos, _, _ = api.AdminRepos(ctx, "github.com", testToken, "OPE", 2); len(repos) != 1 || repos[0].FullName != "o/open" {
		t.Fatalf("filtered = %+v", repos)
	}

	if tok, err := api.RegistrationToken(ctx, "github.com", testToken, "o/mine"); err != nil || tok != "REGTOKEN" {
		t.Fatalf("RegistrationToken = %q, %v", tok, err)
	}
	if tok, err := api.RemoveToken(ctx, "github.com", testToken, "o/mine"); err != nil || tok != "RMTOKEN" {
		t.Fatalf("RemoveToken = %q, %v", tok, err)
	}
	if _, err = api.RegistrationToken(ctx, "github.com", testToken, "o/other"); !errors.Is(err, ErrNoAdmin) {
		t.Fatalf("no admin rights: %v", err)
	}
}

// conformance: CTR-CTL-15
func Test_CTRCTL15_GitHubUnreachableNamesNoToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := API{APIBase: url}.User(context.Background(), "github.com", testToken)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("the error carries the token")
	}
}

// conformance: CTR-CTL-15
func Test_CTRCTL15_AccountTokensStayInPrivateFiles(t *testing.T) {
	a := Accounts{Dir: filepath.Join(t.TempDir(), "ci-accounts")}
	if _, err := a.Token("octo"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("before save: %v", err)
	}
	if err := a.Save("octo", testToken); err != nil {
		t.Fatal(err)
	}
	if err := a.Save("octo", testToken+"2"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Token("octo"); err != nil || got != testToken+"2" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(a.Dir, "octo.token")); err != nil || info.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Fatalf("token file mode: %v %v", info, err)
	}
	if err := a.Save("../escape", "x"); err == nil {
		t.Fatal("an id with a path was accepted")
	}
	if err := a.Delete("octo"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Token("octo"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("after delete: %v", err)
	}
	if got := Hint(testToken); got != "github_pat_1…AAAA" {
		t.Fatalf("Hint = %q", got)
	}
}
