package meshadmin

import "golang.org/x/oauth2"

// oauthHTTPClientKey returns the context key golang.org/x/oauth2 reads to pick
// the HTTP client for token requests.
func oauthHTTPClientKey() any { return oauth2.HTTPClient }
