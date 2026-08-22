package authmw

import (
	"context"
	"fmt"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

// jwksRefreshInterval matches the 1 hour TTL docs/nats-tenant-queue-api/design.md
// §7 commits to: the cache keeps serving its last-fetched keys through a
// Keycloak outage, up to this age.
const jwksRefreshInterval = time.Hour

// JWKSClient supplies the signing keys AuthMiddleware verifies tokens
// against. It owns fetching and caching with background refresh, not token
// verification itself (docs/nats-auth-middleware/design.md §4, Components).
type JWKSClient interface {
	// KeySet returns the current cached key set. Returns an error if no
	// fetch has ever completed successfully (cold-start failure, §7).
	KeySet(ctx context.Context) (jwk.Set, error)
	// Ready reports whether at least one fetch has completed, for
	// GET /healthz/ready (§6).
	Ready(ctx context.Context) bool
	// Issuer is the expected token issuer AuthMiddleware validates against.
	// Empty means issuer is not checked.
	Issuer() string
	// Audience is the expected token audience AuthMiddleware validates
	// against. Empty means audience is not checked.
	Audience() string
}

type jwksCache struct {
	cache            *jwk.Cache
	url              string
	issuer, audience string
}

// NewJWKSCache registers jwksURL with a background-refreshing jwk.Cache and
// returns immediately without waiting for the first fetch, so process
// startup never blocks on Keycloak being reachable (§7, Startup). issuer and
// audience are the values AuthMiddleware validates verified tokens against;
// either may be empty to skip that check.
func NewJWKSCache(ctx context.Context, jwksURL, issuer, audience string) (JWKSClient, error) {
	client := httprc.NewClient()
	cache, err := jwk.NewCache(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("authmw: create jwk cache: %w", err)
	}
	err = cache.Register(ctx, jwksURL,
		jwk.WithConstantInterval(jwksRefreshInterval),
		jwk.WithWaitReady(false),
	)
	if err != nil {
		return nil, fmt.Errorf("authmw: register jwks url: %w", err)
	}
	return &jwksCache{cache: cache, url: jwksURL, issuer: issuer, audience: audience}, nil
}

func (c *jwksCache) KeySet(ctx context.Context) (jwk.Set, error) {
	set, err := c.cache.Lookup(ctx, c.url)
	if err != nil {
		return nil, fmt.Errorf("authmw: jwks not available: %w", err)
	}
	return set, nil
}

func (c *jwksCache) Ready(ctx context.Context) bool {
	return c.cache.Ready(ctx, c.url)
}

func (c *jwksCache) Issuer() string   { return c.issuer }
func (c *jwksCache) Audience() string { return c.audience }
