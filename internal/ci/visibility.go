package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnreachable means GitHub could not be asked.
var ErrUnreachable = fmt.Errorf("连不上 GitHub")

// APIBaseOf is the REST API root of a GitHub host: api.github.com for github.com, /api/v3 on a
// GitHub Enterprise Server.
func APIBaseOf(host string) string {
	if host == "" || host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// RepoIsPublic asks GitHub, without credentials, whether repo (owner/name) is public. A repository
// the anonymous caller cannot see is private or does not exist; both count as not public, and
// registration will fail on its own for the latter.
func RepoIsPublic(ctx context.Context, client *http.Client, host, repo string) (bool, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBaseOf(host)+"/repos/"+repo, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("%w: GitHub 返回 %d", ErrUnreachable, resp.StatusCode)
	}
	var body struct {
		Private    bool   `json:"private"`
		Visibility string `json:"visibility"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return !body.Private && !strings.EqualFold(body.Visibility, "private") && !strings.EqualFold(body.Visibility, "internal"), nil
}
