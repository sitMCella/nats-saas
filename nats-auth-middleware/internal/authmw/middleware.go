package authmw

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lestrrat-go/jwx/v3/jwt"
)

// tenantIDClaim is the OIDC access token claim a Keycloak protocol mapper
// copies the tenant_id user attribute into
// (docs/nats-tenant-queue-api/design.md §6, Naming and identity).
const tenantIDClaim = "tenant_id"

// AuthMiddleware verifies the bearer token's signature, issuer, audience,
// and expiry against jwks, then attaches the verified tenant_id/sub to the
// request's context.Context. It never calls next on rejection:
//   - 503 if the JWKS cache has no key set yet (cold start, §7)
//   - 401 if the token is missing, malformed, or fails verification
//   - 403 if the token verifies but carries no tenant_id claim
//
// (docs/nats-auth-middleware/design.md §6, §9 AC-2).
func AuthMiddleware(jwks JWKSClient) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			keySet, err := jwks.KeySet(r.Context())
			if err != nil {
				writeAuthError(w, http.StatusServiceUnavailable, "jwks_unavailable", "signing keys not yet available")
				return
			}

			raw, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "missing_token", "missing bearer token")
				return
			}

			opts := []jwt.ParseOption{jwt.WithKeySet(keySet)}
			if iss := jwks.Issuer(); iss != "" {
				opts = append(opts, jwt.WithIssuer(iss))
			}
			if aud := jwks.Audience(); aud != "" {
				opts = append(opts, jwt.WithAudience(aud))
			}

			token, err := jwt.Parse([]byte(raw), opts...)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "invalid_token", "token failed verification")
				return
			}

			tenantID, ok := stringClaim(token, tenantIDClaim)
			if !ok || tenantID == "" {
				writeAuthError(w, http.StatusForbidden, "missing_tenant_id", "token carries no tenant_id claim")
				return
			}
			sub, _ := token.Subject()

			ctx := withTenantID(r.Context(), tenantID)
			ctx = withSubject(ctx, sub)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(authHeader string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return "", false
	}
	raw := strings.TrimPrefix(authHeader, prefix)
	return raw, raw != ""
}

func stringClaim(token jwt.Token, name string) (string, bool) {
	var v string
	if err := token.Get(name, &v); err != nil {
		return "", false
	}
	return v, v != ""
}

// writeAuthError matches the error body shape docs/nats-tenant-queue-api/design.md
// §6 fixes for the whole system: {"error": "<short_code>", "message": "..."}.
func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
