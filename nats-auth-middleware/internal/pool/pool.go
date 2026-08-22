// Package pool caches one NATS connection per tenant, keyed strictly by the
// tenant_id read from context.Context (docs/nats-auth-middleware/design.md §4, §6).
package pool

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"golang.org/x/sync/singleflight"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/authmw"
)

// ErrTenantNotProvisioned is returned by Get when the tenant has no
// corresponding Key Vault credential entry. A NATS Account or bucket
// existing with no matching Key Vault entry is inert by design — Pool never
// creates one on the fly (docs/nats-auth-middleware/design.md §7;
// docs/tenant-provisioning/design.md INV-3).
var ErrTenantNotProvisioned = errors.New("pool: tenant not provisioned")

// CredentialFetcher looks up a tenant's NATS user JWT and seed by tenant_id.
// Implementations return ErrTenantNotProvisioned on a Key Vault miss and
// never create anything on the fly (docs/nats-auth-middleware/design.md §4,
// Components — "Key Vault credential fetcher").
type CredentialFetcher interface {
	Fetch(ctx context.Context, tenantID string) (jwt, seed string, err error)
}

const (
	// DefaultTTL is the recommended default from
	// docs/nats-auth-middleware/design.md §12 (Open questions).
	DefaultTTL = 15 * time.Minute
	// DefaultMaxEntries is the recommended default from the same section.
	DefaultMaxEntries = 500
)

// Pool is the per-tenant NATS connection pool
// (docs/nats-auth-middleware/design.md §6).
type Pool interface {
	// Get returns a cached or freshly opened *nats.Conn for the tenant_id
	// carried on ctx. Returns ErrTenantNotProvisioned on a Key Vault miss.
	Get(ctx context.Context) (*nats.Conn, error)
	// Close drains every cached connection so in-flight JetStream writes
	// flush before the process exits (§7, Shutdown).
	Close()
}

type entry struct {
	tenantID string
	conn     *nats.Conn
	expires  time.Time
	elem     *list.Element
}

type pool struct {
	natsURL    string
	fetcher    CredentialFetcher
	ttl        time.Duration
	maxEntries int

	mu      sync.Mutex
	entries map[string]*entry
	lru     *list.List // front = most recently used

	group singleflight.Group
}

// Option configures a Pool built by New.
type Option func(*pool)

// WithTTL overrides DefaultTTL.
func WithTTL(d time.Duration) Option { return func(p *pool) { p.ttl = d } }

// WithMaxEntries overrides DefaultMaxEntries.
func WithMaxEntries(n int) Option { return func(p *pool) { p.maxEntries = n } }

// New builds a Pool that opens connections against natsURL, resolving
// credentials through fetcher on every cache miss.
func New(natsURL string, fetcher CredentialFetcher, opts ...Option) Pool {
	p := &pool{
		natsURL:    natsURL,
		fetcher:    fetcher,
		ttl:        DefaultTTL,
		maxEntries: DefaultMaxEntries,
		entries:    make(map[string]*entry),
		lru:        list.New(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *pool) Get(ctx context.Context) (*nats.Conn, error) {
	tenantID, err := authmw.TenantIDFromContext(ctx)
	if err != nil {
		return nil, err
	}

	if conn, ok := p.lookup(tenantID); ok {
		return conn, nil
	}

	// singleflight collapses concurrent cache misses for the same tenant
	// into one Key Vault fetch and one NATS connect attempt (AC-4). Keying
	// strictly by tenantID also means a miss for tenant B can never wait on
	// or receive tenant A's in-flight setup (INV-3).
	v, err, _ := p.group.Do(tenantID, func() (interface{}, error) {
		if conn, ok := p.lookup(tenantID); ok {
			return conn, nil
		}
		return p.connect(ctx, tenantID)
	})
	if err != nil {
		return nil, err
	}
	return v.(*nats.Conn), nil
}

func (p *pool) lookup(tenantID string) (*nats.Conn, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	e, ok := p.entries[tenantID]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		p.evictLocked(e)
		return nil, false
	}
	p.lru.MoveToFront(e.elem)
	return e.conn, true
}

func (p *pool) connect(ctx context.Context, tenantID string) (*nats.Conn, error) {
	jwtStr, seed, err := p.fetcher.Fetch(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	conn, err := nats.Connect(p.natsURL, nats.UserJWTAndSeed(jwtStr, seed))
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	e := &entry{tenantID: tenantID, conn: conn, expires: time.Now().Add(p.ttl)}
	e.elem = p.lru.PushFront(e)
	p.entries[tenantID] = e

	p.evictOverflowLocked()
	return conn, nil
}

// evictOverflowLocked drops least-recently-used entries once the pool is
// over its configured cap, protecting the process's file descriptor budget
// and the NATS server's total connection count (§8, Shared limits).
func (p *pool) evictOverflowLocked() {
	for len(p.entries) > p.maxEntries {
		oldest := p.lru.Back()
		if oldest == nil {
			return
		}
		p.evictLocked(oldest.Value.(*entry))
	}
}

func (p *pool) evictLocked(e *entry) {
	delete(p.entries, e.tenantID)
	p.lru.Remove(e.elem)
	go e.conn.Drain()
}

func (p *pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, e := range p.entries {
		e.conn.Drain()
	}
	p.entries = make(map[string]*entry)
	p.lru.Init()
}
