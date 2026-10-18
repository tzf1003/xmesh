package ci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/privdir"
)

var accountID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Accounts keeps the GitHub tokens of the saved accounts, one private file per account in a
// directory only the kernel's user can read. The tokens appear in no configuration, response,
// event or log; the configuration holds only the non-secret part of an account (id, host, login).
type Accounts struct {
	Dir string
}

func (a Accounts) path(id string) (string, error) {
	if !accountID.MatchString(id) {
		return "", fmt.Errorf("账号 id 无效")
	}
	return filepath.Join(a.Dir, id+".token"), nil
}

// Save stores the token of account id, replacing an earlier one.
func (a Accounts) Save(id, token string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(a.Dir), 0o700); err != nil {
		return err
	}
	if err = privdir.Ensure(a.Dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(a.Dir, "token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = os.Chmod(tmp.Name(), 0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.WriteString(token + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ErrNoToken means no token is saved for the account.
var ErrNoToken = errors.New("这个账号没有保存令牌")

// Token returns the saved token of account id.
func (a Accounts) Token(id string) (string, error) {
	path, err := a.path(id)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// Delete forgets the token of account id.
func (a Accounts) Delete(id string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Hint is the mask shown for a token: a prefix, an ellipsis and the last four characters.
func Hint(token string) string {
	if len(token) < 5 {
		return "…" + token
	}
	prefix := token[:len(token)-4]
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return prefix + "…" + token[len(token)-4:]
}

// Service is the GitHub side of the saved accounts: the stored tokens and the API calls made
// with them.
type Service struct {
	Store Accounts
	API   API
}

// Verify asks GitHub whose token this is.
func (s Service) Verify(ctx context.Context, host, token string) (string, error) {
	return s.API.User(ctx, host, token)
}

// Save and Delete keep or forget the token of an account.
func (s Service) Save(id, token string) error { return s.Store.Save(id, token) }
func (s Service) Delete(id string) error      { return s.Store.Delete(id) }

// Hint is the mask of the account's saved token, "" when none is saved.
func (s Service) Hint(id string) string {
	token, err := s.Store.Token(id)
	if err != nil {
		return ""
	}
	return Hint(token)
}

// Repos lists the repositories of the account that its token administers.
func (s Service) Repos(ctx context.Context, account config.CIAccount, q string, page int) ([]Repo, int, error) {
	token, err := s.Store.Token(account.ID)
	if err != nil {
		return nil, 0, err
	}
	return s.API.AdminRepos(ctx, hostOrDefault(account.Host), token, q, page)
}

// RegistrationToken asks GitHub for a registration token for repo on behalf of the account.
func (s Service) RegistrationToken(ctx context.Context, account config.CIAccount, repo string) (string, error) {
	token, err := s.Store.Token(account.ID)
	if err != nil {
		return "", err
	}
	return s.API.RegistrationToken(ctx, hostOrDefault(account.Host), token, repo)
}

// RemoveToken asks GitHub for a token that removes a runner of repo on behalf of the account.
func (s Service) RemoveToken(ctx context.Context, account config.CIAccount, repo string) (string, error) {
	token, err := s.Store.Token(account.ID)
	if err != nil {
		return "", err
	}
	return s.API.RemoveToken(ctx, hostOrDefault(account.Host), token, repo)
}

func hostOrDefault(host string) string {
	if host == "" {
		return "github.com"
	}
	return host
}
