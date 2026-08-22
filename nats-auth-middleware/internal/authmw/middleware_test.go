package authmw

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const testIssuer = "https://keycloak.example/realms/natssaas"

type fakeJWKS struct {
	set              jwk.Set
	err              error
	ready            bool
	issuer, audience string
}

func (f *fakeJWKS) KeySet(ctx context.Context) (jwk.Set, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.set, nil
}
func (f *fakeJWKS) Ready(ctx context.Context) bool { return f.ready }
func (f *fakeJWKS) Issuer() string                 { return f.issuer }
func (f *fakeJWKS) Audience() string               { return f.audience }

// testKeys generates an RSA key pair and returns the private jwk.Key (for
// signing) and a jwk.Set containing only the public key (for verification),
// matching how AuthMiddleware is actually driven: it never sees the private
// key.
func testKeys(t *testing.T) (jwk.Key, jwk.Set) {
	t.Helper()

	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	priv, err := jwk.Import(raw)
	if err != nil {
		t.Fatalf("import private key: %v", err)
	}
	if err := priv.Set(jwk.KeyIDKey, "test-key-1"); err != nil {
		t.Fatalf("set kid: %v", err)
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.RS256()); err != nil {
		t.Fatalf("set alg: %v", err)
	}

	pub, err := jwk.PublicKeyOf(priv)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}

	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		t.Fatalf("add key to set: %v", err)
	}

	return priv, set
}

type tokenOpt func(*jwt.Builder)

func withTenantIDClaim(id string) tokenOpt {
	return func(b *jwt.Builder) { b.Claim(tenantIDClaim, id) }
}

func signToken(t *testing.T, priv jwk.Key, opts ...tokenOpt) string {
	t.Helper()

	b := jwt.NewBuilder().
		Issuer(testIssuer).
		Subject("user-123").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour))
	for _, opt := range opts {
		opt(b)
	}
	token, err := b.Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}

	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256(), priv))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

func nextHandler(t *testing.T, called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		tenantID, err := TenantIDFromContext(r.Context())
		if err != nil {
			t.Fatalf("TenantIDFromContext: %v", err)
		}
		if tenantID != "acme-corp" {
			t.Fatalf("tenant_id = %q, want acme-corp", tenantID)
		}
		sub, err := SubjectFromContext(r.Context())
		if err != nil {
			t.Fatalf("SubjectFromContext: %v", err)
		}
		if sub != "user-123" {
			t.Fatalf("sub = %q, want user-123", sub)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func doRequest(handler http.Handler, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/items", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAuthMiddleware_ValidTokenReachesNext(t *testing.T) {
	priv, set := testKeys(t)
	jwks := &fakeJWKS{set: set, ready: true, issuer: testIssuer}

	var called bool
	handler := AuthMiddleware(jwks)(nextHandler(t, &called))

	token := signToken(t, priv, withTenantIDClaim("acme-corp"))
	rec := doRequest(handler, "Bearer "+token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("next handler was not called")
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	_, set := testKeys(t)
	jwks := &fakeJWKS{set: set, ready: true, issuer: testIssuer}

	var called bool
	handler := AuthMiddleware(jwks)(nextHandler(t, &called))

	rec := doRequest(handler, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("next handler must not be called on missing token")
	}
}

func TestAuthMiddleware_ExpiredToken(t *testing.T) {
	priv, set := testKeys(t)
	jwks := &fakeJWKS{set: set, ready: true, issuer: testIssuer}

	var called bool
	handler := AuthMiddleware(jwks)(nextHandler(t, &called))

	b := jwt.NewBuilder().
		Issuer(testIssuer).
		Subject("user-123").
		IssuedAt(time.Now().Add(-2 * time.Hour)).
		Expiration(time.Now().Add(-time.Hour))
	withTenantIDClaim("acme-corp")(b)
	token, err := b.Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256(), priv))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	rec := doRequest(handler, "Bearer "+string(signed))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("next handler must not be called on expired token")
	}
}

func TestAuthMiddleware_BadSignature(t *testing.T) {
	priv, _ := testKeys(t)
	_, otherSet := testKeys(t) // verification set does NOT contain priv's public key

	jwks := &fakeJWKS{set: otherSet, ready: true, issuer: testIssuer}

	var called bool
	handler := AuthMiddleware(jwks)(nextHandler(t, &called))

	token := signToken(t, priv, withTenantIDClaim("acme-corp"))
	rec := doRequest(handler, "Bearer "+token)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("next handler must not be called on bad signature")
	}
}

func TestAuthMiddleware_MissingTenantID(t *testing.T) {
	priv, set := testKeys(t)
	jwks := &fakeJWKS{set: set, ready: true, issuer: testIssuer}

	var called bool
	handler := AuthMiddleware(jwks)(nextHandler(t, &called))

	token := signToken(t, priv) // no tenant_id claim
	rec := doRequest(handler, "Bearer "+token)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("next handler must not be called when tenant_id is missing (AC-2)")
	}
}

func TestAuthMiddleware_JWKSUnavailable(t *testing.T) {
	jwks := &fakeJWKS{err: ErrNoTenantID} // any error stands in for "not ready yet"

	var called bool
	handler := AuthMiddleware(jwks)(nextHandler(t, &called))

	rec := doRequest(handler, "Bearer irrelevant")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if called {
		t.Fatal("next handler must not be called when JWKS is unavailable")
	}
}

func TestTenantIDFromContext_Unpopulated(t *testing.T) {
	if _, err := TenantIDFromContext(context.Background()); err == nil {
		t.Fatal("expected error on unpopulated context")
	}
}

func TestSubjectFromContext_Unpopulated(t *testing.T) {
	if _, err := SubjectFromContext(context.Background()); err == nil {
		t.Fatal("expected error on unpopulated context")
	}
}
