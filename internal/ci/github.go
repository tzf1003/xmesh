package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// API errors the control layer tells apart.
var (
	// ErrRejected means GitHub refused the credential (401).
	ErrRejected = errors.New("GitHub 不接受这个令牌")
	// ErrNoAdmin means the credential is valid but may not manage runners of that repository.
	ErrNoAdmin = errors.New("这个账号对该仓库没有管理员权限，不能注册运行器")
)

// API is the small part of the GitHub REST API this package uses, authenticated with a user's
// token. APIBase, when set, replaces the host's API root (tests).
type API struct {
	Client  *http.Client
	APIBase string
}

func (a API) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (a API) base(host string) string {
	if a.APIBase != "" {
		return strings.TrimRight(a.APIBase, "/")
	}
	return APIBaseOf(host)
}

func (a API) do(ctx context.Context, host, token, method, path string, out any) (http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.base(host)+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrUnreachable, redact(err.Error(), token))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil {
			if err = json.Unmarshal(body, out); err != nil {
				return nil, resp.StatusCode, fmt.Errorf("%w: 无法解析 GitHub 的回答", ErrUnreachable)
			}
		}
		return resp.Header, resp.StatusCode, nil
	}
	return resp.Header, resp.StatusCode, nil
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}

// statusError maps a failed response to the errors above.
func statusError(code int) error {
	switch code {
	case http.StatusUnauthorized:
		return ErrRejected
	case http.StatusForbidden, http.StatusNotFound:
		return ErrNoAdmin
	}
	return fmt.Errorf("%w: GitHub 返回 %d", ErrUnreachable, code)
}

// User returns the login the token belongs to.
func (a API) User(ctx context.Context, host, token string) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	_, code, err := a.do(ctx, host, token, http.MethodGet, "/user", &u)
	if err != nil {
		return "", err
	}
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return "", ErrRejected
	case code != http.StatusOK:
		return "", statusError(code)
	case u.Login == "":
		return "", fmt.Errorf("%w: GitHub 没有给出登录名", ErrUnreachable)
	}
	return u.Login, nil
}

// Repo is a repository the token may administer.
type Repo struct {
	FullName string
	Private  bool
}

// AdminRepos lists one page of the repositories the token has administrator rights on. next is 0
// on the last page. q filters by a case-insensitive substring of the full name.
func (a API) AdminRepos(ctx context.Context, host, token, q string, page int) (repos []Repo, next int, err error) {
	if page < 1 {
		page = 1
	}
	query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}, "sort": {"pushed"}}
	query.Set("affiliation", "owner,collaborator,organization_member")
	var list []struct {
		FullName    string `json:"full_name"`
		Private     bool   `json:"private"`
		Archived    bool   `json:"archived"`
		Permissions struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	header, code, err := a.do(ctx, host, token, http.MethodGet, "/user/repos?"+query.Encode(), &list)
	if err != nil {
		return nil, 0, err
	}
	if code != http.StatusOK {
		return nil, 0, statusError(code)
	}
	q = strings.ToLower(q)
	for _, r := range list {
		if r.Permissions.Admin && !r.Archived && (q == "" || strings.Contains(strings.ToLower(r.FullName), q)) {
			repos = append(repos, Repo{FullName: r.FullName, Private: r.Private})
		}
	}
	if strings.Contains(header.Get("Link"), `rel="next"`) {
		next = page + 1
	}
	return repos, next, nil
}

// RegistrationToken asks GitHub for a registration token for repo (valid one hour).
func (a API) RegistrationToken(ctx context.Context, host, token, repo string) (string, error) {
	return a.runnerToken(ctx, host, token, repo, "registration-token")
}

// RemoveToken asks GitHub for a token that removes a runner of repo.
func (a API) RemoveToken(ctx context.Context, host, token, repo string) (string, error) {
	return a.runnerToken(ctx, host, token, repo, "remove-token")
}

func (a API) runnerToken(ctx context.Context, host, token, repo, kind string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	_, code, err := a.do(ctx, host, token, http.MethodPost, "/repos/"+repo+"/actions/runners/"+kind, &out)
	if err != nil {
		return "", err
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return "", statusError(code)
	}
	if out.Token == "" {
		return "", fmt.Errorf("%w: GitHub 没有给出令牌", ErrUnreachable)
	}
	return out.Token, nil
}
