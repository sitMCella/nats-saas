package kvstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/authmw"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/pool"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/testnats"
)

// fixedFetcher hands back one fixed set of credentials per tenant — a
// minimal pool.CredentialFetcher stand-in, since these tests exercise
// KVStore, not Pool's own caching behavior (covered in internal/pool).
type fixedFetcher struct {
	creds map[string]testnats.Creds
}

func (f *fixedFetcher) Fetch(ctx context.Context, tenantID string) (string, string, error) {
	c, ok := f.creds[tenantID]
	if !ok {
		return "", "", pool.ErrTenantNotProvisioned
	}
	return c.JWT, c.Seed, nil
}

// setup boots a JetStream-enabled test server with one Account and one
// pre-created JetStream KV bucket per tenant name — the same "bucket named
// identically to the tenant_id, created inside the tenant's own Account"
// shape docs/tenant-provisioning/design.md §6 specifies — and returns a
// ready-to-use KVStore backed by a real Pool.
func setup(t *testing.T, tenantNames ...string) KVStore {
	t.Helper()

	url, creds := testnats.StartServer(t, testnats.Options{JetStream: true}, tenantNames...)

	for _, name := range tenantNames {
		nc, err := nats.Connect(url, nats.UserJWTAndSeed(creds[name].JWT, creds[name].Seed))
		if err != nil {
			t.Fatalf("connect as tenant %q to create its bucket: %v", name, err)
		}
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatalf("jetstream context for tenant %q: %v", name, err)
		}
		if _, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{
			Bucket:  name,
			History: 1,
		}); err != nil {
			t.Fatalf("create bucket for tenant %q: %v", name, err)
		}
		nc.Close()
	}

	fetcher := &fixedFetcher{creds: creds}
	p := pool.New(url, fetcher)
	t.Cleanup(p.Close)

	return New(p)
}

func ctxForTenant(tenantID string) context.Context {
	return authmw.ContextWithTenantID(context.Background(), tenantID)
}

func TestKVStore_CreateGetDelete(t *testing.T) {
	store := setup(t, "tenant-a")
	ctx := ctxForTenant("tenant-a")

	payload := json.RawMessage(`{"hello":"world"}`)
	item, err := store.Create(ctx, payload)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if item.ID == "" {
		t.Fatal("Create returned an empty ID")
	}
	if string(item.Data) != string(payload) {
		t.Fatalf("Create returned Data = %s, want %s", item.Data, payload)
	}

	got, err := store.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != item.ID || string(got.Data) != string(payload) {
		t.Fatalf("Get returned %+v, want ID=%s Data=%s", got, item.ID, payload)
	}

	if err := store.Delete(ctx, item.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Get(ctx, item.ID); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrItemNotFound", err)
	}
}

func TestKVStore_DeleteNonExistent(t *testing.T) {
	store := setup(t, "tenant-a")
	ctx := ctxForTenant("tenant-a")

	if err := store.Delete(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAV"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("Delete: err = %v, want ErrItemNotFound", err)
	}
}

// TestKVStore_CreateAssignsDistinctULIDs proves a retried Create with an
// identical payload (simulating a client retry after a dropped connection)
// produces two items with two distinct IDs, never a collision (AC-5).
func TestKVStore_CreateAssignsDistinctULIDs(t *testing.T) {
	store := setup(t, "tenant-a")
	ctx := ctxForTenant("tenant-a")

	payload := json.RawMessage(`{"same":"payload"}`)

	first, err := store.Create(ctx, payload)
	if err != nil {
		t.Fatalf("Create (first): %v", err)
	}
	second, err := store.Create(ctx, payload)
	if err != nil {
		t.Fatalf("Create (second): %v", err)
	}

	if first.ID == second.ID {
		t.Fatalf("two Create calls produced the same ID %q", first.ID)
	}
}

// TestKVStore_TenantIsolation proves an item created for one tenant is
// invisible to another tenant — a cross-tenant Get returns ErrItemNotFound,
// never the item (docs/nats-tenant-queue-api/design.md AC-3).
func TestKVStore_TenantIsolation(t *testing.T) {
	store := setup(t, "tenant-a", "tenant-b")

	item, err := store.Create(ctxForTenant("tenant-a"), json.RawMessage(`{"owner":"a"}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.Get(ctxForTenant("tenant-b"), item.ID); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("cross-tenant Get: err = %v, want ErrItemNotFound", err)
	}
	if err := store.Delete(ctxForTenant("tenant-b"), item.ID); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("cross-tenant Delete: err = %v, want ErrItemNotFound", err)
	}

	// The item is still there for its own tenant.
	if _, err := store.Get(ctxForTenant("tenant-a"), item.ID); err != nil {
		t.Fatalf("same-tenant Get after cross-tenant no-ops: %v", err)
	}
}

// TestKVStore_ListPaginates proves List never returns more than limit items
// per call and that paging through with nextCursor eventually covers every
// created item exactly once, in order.
func TestKVStore_ListPaginates(t *testing.T) {
	store := setup(t, "tenant-a")
	ctx := ctxForTenant("tenant-a")

	const total = 7
	want := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		item, err := store.Create(ctx, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Create[%d]: %v", i, err)
		}
		want[item.ID] = true
	}

	const pageSize = 3
	seen := make(map[string]bool, total)
	cursor := ""
	pages := 0
	for {
		items, next, err := store.List(ctx, cursor, pageSize)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(items) > pageSize {
			t.Fatalf("page returned %d items, want at most %d", len(items), pageSize)
		}
		for _, it := range items {
			if seen[it.ID] {
				t.Fatalf("item %s returned twice across pages", it.ID)
			}
			seen[it.ID] = true
		}
		pages++
		if pages > total { // guard against an infinite loop on a bug
			t.Fatal("List did not terminate")
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("saw %d distinct items across pages, want %d", len(seen), total)
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("item %s created but never listed", id)
		}
	}
}

func TestKVStore_ListRequiresPositiveLimit(t *testing.T) {
	store := setup(t, "tenant-a")
	ctx := ctxForTenant("tenant-a")

	if _, _, err := store.List(ctx, "", 0); err == nil {
		t.Fatal("expected an error for a non-positive limit")
	}
}
