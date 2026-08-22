// Package authmw verifies Keycloak-issued bearer tokens and carries the
// verified tenant_id/sub claims on context.Context (docs/nats-auth-middleware/design.md §4, §6).
package authmw

import (
	"context"
	"errors"
)

type contextKey int

const (
	tenantIDKey contextKey = iota
	subjectKey
)

// ErrNoTenantID is returned by TenantIDFromContext when AuthMiddleware never
// populated the context — a missing middleware step fails loudly, never with
// a zero value that looks valid (docs/nats-auth-middleware/design.md §4, Components).
var ErrNoTenantID = errors.New("authmw: no tenant_id on context")

// ErrNoSubject is returned by SubjectFromContext under the same condition.
var ErrNoSubject = errors.New("authmw: no subject on context")

// TenantIDFromContext returns the tenant_id AuthMiddleware verified and
// attached to ctx. It is the only sanctioned way to read a tenant_id
// (docs/nats-auth-middleware/design.md INV-1, INV-3).
func TenantIDFromContext(ctx context.Context) (string, error) {
	v, ok := ctx.Value(tenantIDKey).(string)
	if !ok || v == "" {
		return "", ErrNoTenantID
	}
	return v, nil
}

// SubjectFromContext returns the token's sub claim, available for audit
// logging only — it never selects a NATS connection or bucket (INV-4).
func SubjectFromContext(ctx context.Context) (string, error) {
	v, ok := ctx.Value(subjectKey).(string)
	if !ok || v == "" {
		return "", ErrNoSubject
	}
	return v, nil
}

func withTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

func withSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, subjectKey, subject)
}

// ContextWithTenantID attaches tenantID to ctx exactly like AuthMiddleware
// does. It exists so tests in other packages (pool, kvstore) can drive
// Pool.Get/KVStore methods without running the full HTTP middleware chain.
// Production code must never call this outside AuthMiddleware itself — the
// verified tenant_id claim is the only legitimate source (INV-1).
func ContextWithTenantID(ctx context.Context, tenantID string) context.Context {
	return withTenantID(ctx, tenantID)
}
