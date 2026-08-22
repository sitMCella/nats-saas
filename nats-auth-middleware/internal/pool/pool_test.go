package pool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/authmw"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/testnats"
)

func startTestServer(t *testing.T, tenantNames ...string) (string, map[string]testnats.Creds) {
	t.Helper()
	return testnats.StartServer(t, testnats.Options{}, tenantNames...)
}

// fakeFetcher serves pre-minted credentials and counts fetches per tenant.
type fakeFetcher struct {
	mu     sync.Mutex
	creds  map[string]testnats.Creds
	counts map[string]int
	delay  time.Duration // simulates a slow Key Vault round trip
}

func newFakeFetcher(creds map[string]testnats.Creds) *fakeFetcher {
	return &fakeFetcher{creds: creds, counts: make(map[string]int)}
}

func (f *fakeFetcher) Fetch(ctx context.Context, tenantID string) (string, string, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.counts[tenantID]++
	f.mu.Unlock()

	c, ok := f.creds[tenantID]
	if !ok {
		return "", "", ErrTenantNotProvisioned
	}
	return c.JWT, c.Seed, nil
}

func (f *fakeFetcher) count(tenantID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[tenantID]
}

func ctxForTenant(tenantID string) context.Context {
	return authmw.ContextWithTenantID(context.Background(), tenantID)
}

func TestPool_CacheHitAvoidsRefetch(t *testing.T) {
	url, creds := startTestServer(t, "tenant-a")
	fetcher := newFakeFetcher(creds)
	p := New(url, fetcher)
	defer p.Close()

	ctx := ctxForTenant("tenant-a")

	conn1, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	conn2, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if conn1 != conn2 {
		t.Fatal("expected cached connection to be reused")
	}
	if got := fetcher.count("tenant-a"); got != 1 {
		t.Fatalf("fetch count = %d, want 1", got)
	}
}

// TestPool_TenantIsolation proves a cache-miss lookup for tenant B never
// returns tenant A's connection, even when the two Gets race (INV-3, AC-3).
func TestPool_TenantIsolation(t *testing.T) {
	url, creds := startTestServer(t, "tenant-a", "tenant-b")
	fetcher := newFakeFetcher(creds)
	p := New(url, fetcher)
	defer p.Close()

	var wg sync.WaitGroup
	var connA, connB *nats.Conn
	var errA, errB error

	wg.Add(2)
	go func() {
		defer wg.Done()
		connA, errA = p.Get(ctxForTenant("tenant-a"))
	}()
	go func() {
		defer wg.Done()
		connB, errB = p.Get(ctxForTenant("tenant-b"))
	}()
	wg.Wait()

	if errA != nil || errB != nil {
		t.Fatalf("Get errors: A=%v B=%v", errA, errB)
	}
	if connA == connB {
		t.Fatal("tenant A and tenant B resolved to the same connection")
	}

	// Repeated lookups keep returning the tenant's own connection, never
	// the other's.
	for i := 0; i < 10; i++ {
		gotA, err := p.Get(ctxForTenant("tenant-a"))
		if err != nil {
			t.Fatalf("Get(a): %v", err)
		}
		if gotA != connA {
			t.Fatal("tenant A's cached connection changed identity unexpectedly")
		}
		gotB, err := p.Get(ctxForTenant("tenant-b"))
		if err != nil {
			t.Fatalf("Get(b): %v", err)
		}
		if gotB != connB {
			t.Fatal("tenant B's cached connection changed identity unexpectedly")
		}
	}
}

// TestPool_ConcurrentCacheMissSingleFetch proves concurrent cache misses for
// the same tenant collapse into exactly one Key Vault fetch and one NATS
// connect (AC-4).
func TestPool_ConcurrentCacheMissSingleFetch(t *testing.T) {
	url, creds := startTestServer(t, "tenant-a")
	fetcher := newFakeFetcher(creds)
	fetcher.delay = 50 * time.Millisecond
	p := New(url, fetcher)
	defer p.Close()

	const n = 20
	var wg sync.WaitGroup
	conns := make([]*nats.Conn, n)
	errs := make([]error, n)

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			conns[i], errs[i] = p.Get(ctxForTenant("tenant-a"))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Get[%d]: %v", i, err)
		}
		if conns[i] != conns[0] {
			t.Fatalf("Get[%d] returned a different connection than Get[0]", i)
		}
	}
	if got := fetcher.count("tenant-a"); got != 1 {
		t.Fatalf("fetch count = %d, want 1 (concurrent misses did not collapse)", got)
	}
}

func TestPool_TTLEviction(t *testing.T) {
	url, creds := startTestServer(t, "tenant-a")
	fetcher := newFakeFetcher(creds)
	p := New(url, fetcher, WithTTL(20*time.Millisecond))
	defer p.Close()

	ctx := ctxForTenant("tenant-a")

	if _, err := p.Get(ctx); err != nil {
		t.Fatalf("Get: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := p.Get(ctx); err != nil {
		t.Fatalf("Get after TTL: %v", err)
	}

	if got := fetcher.count("tenant-a"); got != 2 {
		t.Fatalf("fetch count after TTL expiry = %d, want 2", got)
	}
}

func TestPool_MaxEntriesEvictsLeastRecentlyUsed(t *testing.T) {
	url, creds := startTestServer(t, "tenant-a", "tenant-b")
	fetcher := newFakeFetcher(creds)
	p := New(url, fetcher, WithMaxEntries(1))
	defer p.Close()

	if _, err := p.Get(ctxForTenant("tenant-a")); err != nil {
		t.Fatalf("Get(a): %v", err)
	}
	if _, err := p.Get(ctxForTenant("tenant-b")); err != nil {
		t.Fatalf("Get(b): %v", err)
	}
	// tenant-a should have been evicted to make room for tenant-b.
	if _, err := p.Get(ctxForTenant("tenant-a")); err != nil {
		t.Fatalf("Get(a) again: %v", err)
	}

	if got := fetcher.count("tenant-a"); got != 2 {
		t.Fatalf("tenant-a fetch count = %d, want 2 (expected eviction to force a refetch)", got)
	}
}

func TestPool_TenantNotProvisioned(t *testing.T) {
	url, creds := startTestServer(t, "tenant-a")
	fetcher := newFakeFetcher(creds)
	p := New(url, fetcher)
	defer p.Close()

	_, err := p.Get(ctxForTenant("unknown-tenant"))
	if !errors.Is(err, ErrTenantNotProvisioned) {
		t.Fatalf("err = %v, want ErrTenantNotProvisioned", err)
	}
}
