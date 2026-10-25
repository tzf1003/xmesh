package cfadmin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Account is a Cloudflare account the token can see.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Zone is a domain the token can see, with the account that owns it.
type Zone struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AccountID string `json:"account_id"`
}

const (
	discoverPageSize = 50
	discoverMaxPages = 10
)

// Discover lists the accounts and zones a token can see, so a user does not have to look their IDs up.
// Listing accounts needs Account Settings (Read) and listing zones needs Zone (Read); when accounts cannot
// be listed they are taken from the zones. It fails only when neither list works.
func (c *Client) Discover(ctx context.Context) ([]Account, []Zone, error) {
	zones, zoneErr := c.listZones(ctx)
	accounts, accErr := c.listAccounts(ctx)
	if accErr != nil && zoneErr != nil {
		return nil, nil, accErr
	}
	if accErr != nil || len(accounts) == 0 {
		accounts = accountsOf(zones, accounts)
	}
	return accounts, zones, nil
}

func (c *Client) listAccounts(ctx context.Context) ([]Account, error) {
	var all []Account
	for page := 1; page <= discoverMaxPages; page++ {
		var got []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := c.do(ctx, "GET", fmt.Sprintf("/accounts?per_page=%d&page=%d", discoverPageSize, page), nil, &got); err != nil {
			return nil, err
		}
		for _, a := range got {
			all = append(all, Account{ID: a.ID, Name: a.Name})
		}
		if len(got) < discoverPageSize {
			break
		}
	}
	return all, nil
}

func (c *Client) listZones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	for page := 1; page <= discoverMaxPages; page++ {
		var got []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Account struct {
				ID string `json:"id"`
			} `json:"account"`
		}
		q := url.Values{"per_page": {fmt.Sprint(discoverPageSize)}, "page": {fmt.Sprint(page)}, "status": {"active"}}
		if err := c.do(ctx, "GET", "/zones?"+q.Encode(), nil, &got); err != nil {
			return nil, err
		}
		for _, z := range got {
			all = append(all, Zone{ID: z.ID, Name: strings.ToLower(z.Name), AccountID: z.Account.ID})
		}
		if len(got) < discoverPageSize {
			break
		}
	}
	return all, nil
}

// accountsOf keeps the names known for an account and adds the owners of zones that were not listed.
func accountsOf(zones []Zone, known []Account) []Account {
	seen := map[string]bool{}
	out := append([]Account(nil), known...)
	for _, a := range known {
		seen[a.ID] = true
	}
	for _, z := range zones {
		if z.AccountID != "" && !seen[z.AccountID] {
			seen[z.AccountID] = true
			out = append(out, Account{ID: z.AccountID})
		}
	}
	return out
}

// Resolve picks the account and zone for domain from what Discover found. An account ID the caller already
// has narrows the choice; otherwise the domain must belong to exactly one account. The zone is the one whose
// name is domain or its longest parent.
func Resolve(accounts []Account, zones []Zone, accountID, domain string) (Account, Zone, error) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	var best []Zone
	longest := 0
	for _, z := range zones {
		if accountID != "" && z.AccountID != accountID {
			continue
		}
		if domain != z.Name && !strings.HasSuffix(domain, "."+z.Name) {
			continue
		}
		switch {
		case len(z.Name) > longest:
			best, longest = []Zone{z}, len(z.Name)
		case len(z.Name) == longest:
			best = append(best, z)
		}
	}
	switch len(best) {
	case 0:
		names := make([]string, 0, len(zones))
		for _, z := range zones {
			names = append(names, z.Name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return Account{}, Zone{}, errors.New("这个令牌看不到任何域名；确认它有 Zone（Read）权限，或手动指定 --account 和 --zone")
		}
		return Account{}, Zone{}, fmt.Errorf("令牌可见的域名里没有 %s 的区域（可见：%s）", domain, strings.Join(names, "、"))
	case 1:
	default:
		return Account{}, Zone{}, fmt.Errorf("%s 同时出现在多个账号下，请用 --account 指定", domain)
	}
	zone := best[0]
	account := Account{ID: zone.AccountID}
	for _, a := range accounts {
		if a.ID == zone.AccountID {
			account = a
		}
	}
	return account, zone, nil
}
