// Package auth (context.go): typed request-scoped auth payload + helpers.
package auth

import "context"

type ctxKey int

const authCtxKey ctxKey = 0

// AuthContext is the payload placed on the request context when Verify
// succeeds. Downstream handlers (proxy, audit, idempotency) read it via
// FromContext. KeyPrefix is safe to log (last 4 chars only per D-A2).
type AuthContext struct {
	TenantID  string
	APIKeyID  string
	DataClass DataClass
	KeyPrefix string // "ifix_sk_****abcd"
}

// WithContext stashes ac on ctx.
func WithContext(ctx context.Context, ac AuthContext) context.Context {
	return context.WithValue(ctx, authCtxKey, ac)
}

// FromContext returns the AuthContext and true if present.
func FromContext(ctx context.Context) (AuthContext, bool) {
	ac, ok := ctx.Value(authCtxKey).(AuthContext)
	return ac, ok
}

// MustFromContext panics if no AuthContext is present. Use only in routes
// protected by auth.Middleware.
func MustFromContext(ctx context.Context) AuthContext {
	ac, ok := FromContext(ctx)
	if !ok {
		panic("auth: no AuthContext in ctx — middleware not applied")
	}
	return ac
}
