package httpapi

import (
	"net/http"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/authmw"
)

// NewRouter wires the health endpoints (unauthenticated) and the /v1/items
// routes (behind AuthMiddleware) into one http.Handler.
func NewRouter(jwks authmw.JWKSClient, health *HealthHandlers, items *ItemHandlers) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz/live", health.Live)
	mux.HandleFunc("GET /healthz/ready", health.Ready)

	authed := http.NewServeMux()
	authed.HandleFunc("POST /v1/items", items.Create)
	authed.HandleFunc("GET /v1/items", items.List)
	authed.HandleFunc("GET /v1/items/{id}", items.Get)
	authed.HandleFunc("DELETE /v1/items/{id}", items.Delete)

	mux.Handle("/v1/", authmw.AuthMiddleware(jwks)(authed))

	return mux
}
