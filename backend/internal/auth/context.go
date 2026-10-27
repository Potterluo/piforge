package auth

import (
	"context"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// userCtxKey is unexported so only this package can put a user in a context.
// That is what makes MustUser trustworthy: if the value is present, it came
// from RequireUser.
type userCtxKey struct{}

// WithUser returns a context carrying an authenticated user. Only RequireUser
// and tests should need it.
func WithUser(ctx context.Context, u *contracts.User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, u)
}

// UserFrom returns the authenticated user, if any.
func UserFrom(ctx context.Context) (*contracts.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(*contracts.User)
	if !ok || u == nil {
		return nil, false
	}
	return u, true
}

// MustUser returns the authenticated user or panics.
//
// The panic is the point: a handler that reaches a repository without passing
// through RequireUser is a programming error, and failing loudly turns a
// potential cross-user data leak into a 500 that the recover middleware logs.
// It is never a normal request path (PRD §8.1).
func MustUser(ctx context.Context) *contracts.User {
	u, ok := UserFrom(ctx)
	if !ok {
		panic("auth: MustUser called on a context without a user (missing RequireUser?)")
	}
	return u
}

// MustUserID is MustUser's id accessor.
func MustUserID(ctx context.Context) string { return MustUser(ctx).ID }
