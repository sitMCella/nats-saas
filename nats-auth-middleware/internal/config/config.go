// Package config reads process configuration from environment variables —
// flags or env vars, not hardcoded constants, per the tunables
// docs/nats-auth-middleware/design.md §12 calls out (Pool TTL, max cached
// connections).
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/pool"
)

// Config holds every value main.go needs to wire the process together.
type Config struct {
	ListenAddr string

	KeycloakJWKSURL  string
	KeycloakIssuer   string
	KeycloakAudience string // optional; empty skips audience validation

	NATSURL string

	KeyVaultURL string

	PoolTTL        time.Duration
	PoolMaxEntries int

	ShutdownGrace time.Duration
}

// Load reads Config from the environment, applying the same defaults
// docs/nats-auth-middleware/design.md §12 recommends for the pool, and the
// 10 second termination grace docs/nats-tenant-queue-api/design.md §7 fixes
// for the whole system.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr: getEnvDefault("LISTEN_ADDR", ":8080"),

		KeycloakJWKSURL:  os.Getenv("KEYCLOAK_JWKS_URL"),
		KeycloakIssuer:   os.Getenv("KEYCLOAK_ISSUER"),
		KeycloakAudience: os.Getenv("KEYCLOAK_AUDIENCE"),

		NATSURL: os.Getenv("NATS_URL"),

		KeyVaultURL: os.Getenv("KEY_VAULT_URL"),

		PoolTTL:        pool.DefaultTTL,
		PoolMaxEntries: pool.DefaultMaxEntries,

		ShutdownGrace: 10 * time.Second,
	}

	for _, req := range []struct {
		name  string
		value string
	}{
		{"KEYCLOAK_JWKS_URL", cfg.KeycloakJWKSURL},
		{"KEYCLOAK_ISSUER", cfg.KeycloakIssuer},
		{"NATS_URL", cfg.NATSURL},
		{"KEY_VAULT_URL", cfg.KeyVaultURL},
	} {
		if req.value == "" {
			return Config{}, fmt.Errorf("config: %s is required", req.name)
		}
	}

	if v := os.Getenv("POOL_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: POOL_TTL: %w", err)
		}
		cfg.PoolTTL = d
	}

	if v := os.Getenv("POOL_MAX_ENTRIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: POOL_MAX_ENTRIES must be a positive integer, got %q", v)
		}
		cfg.PoolMaxEntries = n
	}

	if v := os.Getenv("SHUTDOWN_GRACE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: SHUTDOWN_GRACE: %w", err)
		}
		cfg.ShutdownGrace = d
	}

	return cfg, nil
}

func getEnvDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
