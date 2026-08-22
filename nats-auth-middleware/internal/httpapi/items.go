// Package httpapi is the thin HTTP handler layer wired on top of this
// document's Go interfaces. The HTTP contract itself (routes, JSON shapes,
// status codes) is fixed by docs/nats-tenant-queue-api/design.md §6; this
// package only maps that contract onto authmw.AuthMiddleware, pool.Pool, and
// kvstore.KVStore.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/authmw"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/kvstore"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/pool"
)

// maxPayloadBytes matches the default 1 MiB per-item payload limit
// docs/nats-tenant-queue-api/design.md §8 documents (NATS's own default max
// message size).
const maxPayloadBytes = 1 << 20

// ItemHandlers implements the /v1/items routes
// (docs/nats-tenant-queue-api/design.md §6).
type ItemHandlers struct {
	store kvstore.KVStore
}

func NewItemHandlers(store kvstore.KVStore) *ItemHandlers {
	return &ItemHandlers{store: store}
}

type itemResponse struct {
	ID        string          `json:"id"`
	CreatedAt string          `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

type itemSummaryResponse struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

type listResponse struct {
	Items      []itemSummaryResponse `json:"items"`
	NextCursor string                `json:"next_cursor"`
}

// Create handles POST /v1/items.
func (h *ItemHandlers) Create(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPayloadBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds the maximum item size")
		return
	}
	if !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a valid JSON value")
		return
	}

	item, err := h.store.Create(r.Context(), json.RawMessage(body))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, itemResponse{
		ID:        item.ID,
		CreatedAt: item.CreatedAt.UTC().Format(rfc3339Milli),
		Data:      item.Data,
	})
}

// List handles GET /v1/items?cursor=&limit=. limit is required — no
// endpoint returns an unbounded result set
// (docs/nats-auth-middleware/design.md §5, Requirements).
func (h *ItemHandlers) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limitStr := q.Get("limit")
	limit, err := strconv.Atoi(limitStr)
	if limitStr == "" || err != nil || limit <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit is required and must be a positive integer")
		return
	}

	cursor := q.Get("cursor")

	items, nextCursor, err := h.store.List(r.Context(), cursor, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	resp := listResponse{Items: make([]itemSummaryResponse, 0, len(items)), NextCursor: nextCursor}
	for _, it := range items {
		resp.Items = append(resp.Items, itemSummaryResponse{
			ID:        it.ID,
			CreatedAt: it.CreatedAt.UTC().Format(rfc3339Milli),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/items/{id}.
func (h *ItemHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	item, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, itemResponse{
		ID:        item.ID,
		CreatedAt: item.CreatedAt.UTC().Format(rfc3339Milli),
		Data:      item.Data,
	})
}

// Delete handles DELETE /v1/items/{id}.
func (h *ItemHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.store.Delete(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// writeStoreError maps this layer's typed errors onto the status codes
// docs/nats-auth-middleware/design.md §7 documents.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, kvstore.ErrItemNotFound):
		// Indistinguishable from "belongs to another tenant" by design
		// (docs/nats-tenant-queue-api/design.md AC-3).
		writeError(w, http.StatusNotFound, "not_found", "item not found")
	case errors.Is(err, pool.ErrTenantNotProvisioned):
		writeError(w, http.StatusServiceUnavailable, "tenant_not_provisioned", "tenant has no provisioned NATS account")
	case errors.Is(err, authmw.ErrNoTenantID):
		// Should never happen behind AuthMiddleware; fails loudly rather
		// than silently resolving an empty tenant.
		writeError(w, http.StatusInternalServerError, "internal_error", "no verified tenant on request context")
	default:
		writeError(w, http.StatusBadGateway, "nats_error", "the datastore request failed")
	}
}
