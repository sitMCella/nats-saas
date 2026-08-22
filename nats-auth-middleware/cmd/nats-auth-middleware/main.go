// Command nats-auth-middleware is the REST API binary: AuthN/AuthZ
// middleware, per-tenant NATS connection pool, and JetStream KV operations
// behind the /v1/items routes (docs/nats-auth-middleware/design.md;
// docs/nats-tenant-queue-api/design.md).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/authmw"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/config"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/httpapi"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/kvstore"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/pool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// AuthMiddleware's JWKS cache: registered here, never blocking startup
	// on Keycloak being reachable (§7, Startup).
	jwksClient, err := authmw.NewJWKSCache(ctx, cfg.KeycloakJWKSURL, cfg.KeycloakIssuer, cfg.KeycloakAudience)
	if err != nil {
		return err
	}

	// Azure AD Workload Identity — no static Azure credential anywhere in
	// this process (docs/nats-auth-middleware/design.md §8).
	credential, err := azidentity.NewWorkloadIdentityCredential(nil)
	if err != nil {
		return err
	}
	fetcher, err := pool.NewKeyVaultFetcher(cfg.KeyVaultURL, credential)
	if err != nil {
		return err
	}

	natsPool := pool.New(cfg.NATSURL, fetcher,
		pool.WithTTL(cfg.PoolTTL),
		pool.WithMaxEntries(cfg.PoolMaxEntries),
	)
	defer natsPool.Close()

	store := kvstore.New(natsPool)

	health := httpapi.NewHealthHandlers(jwksClient)
	items := httpapi.NewItemHandlers(store)
	router := httpapi.NewRouter(jwksClient, health, items)

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: router,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// Stop accepting new connections, wait up to ShutdownGrace for in-flight
	// requests to finish, then drain pooled NATS connections so pending KV
	// writes flush (§7, Shutdown).
	logger.Info("shutting down", "grace", cfg.ShutdownGrace)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return <-serveErr
}
