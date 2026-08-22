package httpapi

import (
	"context"
	"net/http"
)

// readinessChecker reports whether the process is ready to accept traffic —
// the JWKS cache's readiness only, never NATS or Key Vault reachability,
// since those are per-tenant, cache-miss-time dependencies, not a
// precondition for accepting any traffic at all
// (docs/nats-auth-middleware/design.md §6, §7 Startup).
type readinessChecker interface {
	Ready(ctx context.Context) bool
}

// HealthHandlers owns GET /healthz/live and GET /healthz/ready. Both are
// unauthenticated by design (§4, Components).
type HealthHandlers struct {
	ready readinessChecker
}

func NewHealthHandlers(ready readinessChecker) *HealthHandlers {
	return &HealthHandlers{ready: ready}
}

// Live returns 200 as soon as the HTTP server is accepting connections;
// checks nothing else.
func (h *HealthHandlers) Live(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Ready returns 200 once the JWKS cache has completed at least one
// successful fetch, 503 otherwise.
func (h *HealthHandlers) Ready(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Ready(r.Context()) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
