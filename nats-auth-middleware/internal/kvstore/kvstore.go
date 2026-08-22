// Package kvstore translates Create/List/Get/Delete calls into JetStream
// Key-Value operations against a tenant's bucket, obtained through Pool on
// every call (docs/nats-auth-middleware/design.md §4, §6).
package kvstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/oklog/ulid/v2"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/authmw"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/pool"
)

// Item is a stored record. ID is a server-generated ULID (INV-2): no method
// on this package accepts a caller-supplied item ID for create.
type Item struct {
	ID        string
	CreatedAt time.Time
	Data      json.RawMessage
}

// ItemSummary is the shape List returns — no payload, so list responses
// stay small (docs/nats-tenant-queue-api/design.md §6). Clients fetch full
// items via Get.
type ItemSummary struct {
	ID        string
	CreatedAt time.Time
}

// ErrItemNotFound is returned by Get and Delete when id does not exist in
// the caller's tenant bucket — including when it exists only in a different
// tenant's bucket, which is indistinguishable by design
// (docs/nats-tenant-queue-api/design.md AC-3).
var ErrItemNotFound = errors.New("kvstore: item not found")

// maxCreateAttempts bounds the retry loop on a ULID collision (astronomically
// unlikely, but Create never silently drops a write on one).
const maxCreateAttempts = 3

// KVStore is the JetStream KV operations layer
// (docs/nats-auth-middleware/design.md §6).
type KVStore interface {
	Create(ctx context.Context, payload json.RawMessage) (Item, error)
	List(ctx context.Context, cursor string, limit int) (items []ItemSummary, nextCursor string, err error)
	Get(ctx context.Context, id string) (Item, error)
	Delete(ctx context.Context, id string) error
}

type store struct {
	pool pool.Pool
}

// New builds a KVStore that resolves a tenant's *nats.Conn through p on
// every call and operates on the JetStream KV bucket named identically to
// the tenant_id (docs/tenant-provisioning/design.md §6).
func New(p pool.Pool) KVStore {
	return &store{pool: p}
}

func (s *store) bucket(ctx context.Context) (jetstream.KeyValue, string, error) {
	tenantID, err := authmw.TenantIDFromContext(ctx)
	if err != nil {
		return nil, "", err
	}
	nc, err := s.pool.Get(ctx)
	if err != nil {
		return nil, "", err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, "", fmt.Errorf("kvstore: create jetstream context: %w", err)
	}
	kv, err := js.KeyValue(ctx, tenantID)
	if err != nil {
		return nil, "", fmt.Errorf("kvstore: open bucket %q: %w", tenantID, err)
	}
	return kv, tenantID, nil
}

// Create generates a ULID, writes payload under it, and blocks until
// JetStream acknowledges the write before returning (INV-2; every write
// blocks on acknowledgment, docs/nats-auth-middleware/design.md §5
// Requirements). A retried Create after a dropped connection produces a
// second item with a new ULID, never a collision (§7, AC-5).
func (s *store) Create(ctx context.Context, payload json.RawMessage) (Item, error) {
	kv, _, err := s.bucket(ctx)
	if err != nil {
		return Item{}, err
	}

	var id string
	var createdAt time.Time
	for attempt := 0; attempt < maxCreateAttempts; attempt++ {
		id, createdAt, err = newULID()
		if err != nil {
			return Item{}, fmt.Errorf("kvstore: generate item id: %w", err)
		}
		_, err = kv.Create(ctx, id, payload)
		if err == nil {
			return Item{ID: id, CreatedAt: createdAt, Data: payload}, nil
		}
		if !errors.Is(err, jetstream.ErrKeyExists) {
			return Item{}, fmt.Errorf("kvstore: create item: %w", err)
		}
	}
	return Item{}, fmt.Errorf("kvstore: create item: %w", err)
}

// List returns tenant items ordered by ID (chronological, since ULIDs are
// time-sortable), paginated by a required cursor and limit — no call can
// return an unbounded result set (§5, Requirements).
func (s *store) List(ctx context.Context, cursor string, limit int) ([]ItemSummary, string, error) {
	if limit <= 0 {
		return nil, "", fmt.Errorf("kvstore: limit must be positive, got %d", limit)
	}

	kv, _, err := s.bucket(ctx)
	if err != nil {
		return nil, "", err
	}

	lister, err := kv.ListKeys(ctx, jetstream.IgnoreDeletes())
	if err != nil {
		if errors.Is(err, jetstream.ErrNoKeysFound) {
			return []ItemSummary{}, "", nil
		}
		return nil, "", fmt.Errorf("kvstore: list keys: %w", err)
	}

	keys := make([]string, 0)
	for key := range lister.Keys() {
		if key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	items := make([]ItemSummary, 0, limit)
	for i, key := range keys {
		if i >= limit {
			return items, keys[limit-1], nil
		}
		id, err := ulid.ParseStrict(key)
		if err != nil {
			continue // not one of our keys; skip rather than fail the whole page
		}
		items = append(items, ItemSummary{ID: key, CreatedAt: ulid.Time(id.Time())})
	}
	return items, "", nil
}

// Get returns the item stored under id in the caller's tenant bucket, or
// ErrItemNotFound.
func (s *store) Get(ctx context.Context, id string) (Item, error) {
	kv, _, err := s.bucket(ctx)
	if err != nil {
		return Item{}, err
	}

	entry, err := kv.Get(ctx, id)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
			return Item{}, ErrItemNotFound
		}
		return Item{}, fmt.Errorf("kvstore: get item %q: %w", id, err)
	}

	parsed, err := ulid.ParseStrict(id)
	createdAt := entry.Created()
	if err == nil {
		createdAt = ulid.Time(parsed.Time())
	}

	return Item{ID: id, CreatedAt: createdAt, Data: json.RawMessage(entry.Value())}, nil
}

// Delete removes id from the caller's tenant bucket, or returns
// ErrItemNotFound if it does not already exist there.
func (s *store) Delete(ctx context.Context, id string) error {
	kv, _, err := s.bucket(ctx)
	if err != nil {
		return err
	}

	if _, err := kv.Get(ctx, id); err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
			return ErrItemNotFound
		}
		return fmt.Errorf("kvstore: get item %q before delete: %w", id, err)
	}

	if err := kv.Delete(ctx, id); err != nil {
		return fmt.Errorf("kvstore: delete item %q: %w", id, err)
	}
	return nil
}

func newULID() (string, time.Time, error) {
	now := time.Now()
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return "", time.Time{}, err
	}
	return id.String(), now, nil
}
