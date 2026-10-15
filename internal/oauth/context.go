package oauth

import "context"

type tokenKey struct{}

func withToken(ctx context.Context, token Token) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}
func FromContext(ctx context.Context) (Token, bool) {
	t, ok := ctx.Value(tokenKey{}).(Token)
	return t, ok
}
