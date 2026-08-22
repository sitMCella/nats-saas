package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	natsjs "github.com/nats-io/nats.go/jetstream"

	"github.com/nats-io/nats.go"

	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/kvstore"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/pool"
	"github.com/sitMCella/nats-saas/nats-auth-middleware/internal/testnats"
)

const testIssuer = "https://keycloak.example/realms/natssaas"

// stubJWKS lets tests sign tokens locally and verify them against the exact
// same key, without running a real Keycloak.
type stubJWKS struct {
	set jwk.Set
}

func (s *stubJWKS) KeySet(ctx context.Context) (jwk.Set, error) { return s.set, nil }
func (s *stubJWKS) Ready(ctx context.Context) bool              { return true }
func (s *stubJWKS) Issuer() string                              { return testIssuer }
func (s *stubJWKS) Audience() string                            { return "" }

func newSignedToken(t *testing.T, priv jwk.Key, tenantID string) string {
	t.Helper()

	b := jwt.NewBuilder().
		Issuer(testIssuer).
		Subject("user-123").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour))
	if tenantID != "" {
		b = b.Claim("tenant_id", tenantID)
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

func newTestKeys(t *testing.T) (jwk.Key, jwk.Set) {
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

// newTestRouter wires the full stack this document's Go interfaces
// describe — AuthMiddleware, Pool, KVStore — behind a real HTTP router,
// exactly as docs/nats-auth-middleware/design.md §10 (Test approach)
// describes for its end-to-end test.
func newTestRouter(t *testing.T, tenantNames ...string) (http.Handler, jwk.Key) {
	t.Helper()

	priv, set := newTestKeys(t)
	jwks := &stubJWKS{set: set}

	url, creds := testnats.StartServer(t, testnats.Options{JetStream: true}, tenantNames...)
	for _, name := range tenantNames {
		nc, err := nats.Connect(url, nats.UserJWTAndSeed(creds[name].JWT, creds[name].Seed))
		if err != nil {
			t.Fatalf("connect as tenant %q: %v", name, err)
		}
		js, err := natsjs.New(nc)
		if err != nil {
			t.Fatalf("jetstream context: %v", err)
		}
		if _, err := js.CreateKeyValue(context.Background(), natsjs.KeyValueConfig{Bucket: name, History: 1}); err != nil {
			t.Fatalf("create bucket for %q: %v", name, err)
		}
		nc.Close()
	}

	p := pool.New(url, &fixedFetcher{creds: creds})
	t.Cleanup(p.Close)

	store := kvstore.New(p)
	router := NewRouter(jwks, NewHealthHandlers(jwks), NewItemHandlers(store))
	return router, priv
}

// TestEndToEnd_CreateListGetDelete proves AC-1 (a verified tenant_id
// reaches KVStore and no other value is reachable) end to end through the
// real HTTP surface.
func TestEndToEnd_CreateListGetDelete(t *testing.T) {
	router, priv := newTestRouter(t, "acme-corp")
	token := newSignedToken(t, priv, "acme-corp")

	// Create
	createReq := httptest.NewRequest(http.MethodPost, "/v1/items", strings.NewReader(`{"hello":"world"}`))
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("Create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created itemResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create response has empty id")
	}

	// Get
	getReq := httptest.NewRequest(http.MethodGet, "/v1/items/"+created.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("Get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	// List
	listReq := httptest.NewRequest(http.MethodGet, "/v1/items?limit=10", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("List status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var list listResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("List = %+v, want exactly the created item", list)
	}

	// Delete
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/items/"+created.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("Delete status = %d, body = %s", delRec.Code, delRec.Body.String())
	}

	// Get after delete -> 404
	getAfterReq := httptest.NewRequest(http.MethodGet, "/v1/items/"+created.ID, nil)
	getAfterReq.Header.Set("Authorization", "Bearer "+token)
	getAfterRec := httptest.NewRecorder()
	router.ServeHTTP(getAfterRec, getAfterReq)
	if getAfterRec.Code != http.StatusNotFound {
		t.Fatalf("Get after Delete status = %d, want 404", getAfterRec.Code)
	}
}

// TestEndToEnd_CrossTenantReadReturns404 proves AC-3 from
// docs/nats-tenant-queue-api/design.md: an item ID that exists only in
// another tenant's bucket returns 404, never 403 or 200.
func TestEndToEnd_CrossTenantReadReturns404(t *testing.T) {
	router, priv := newTestRouter(t, "tenant-a", "tenant-b")

	tokenA := newSignedToken(t, priv, "tenant-a")
	tokenB := newSignedToken(t, priv, "tenant-b")

	createReq := httptest.NewRequest(http.MethodPost, "/v1/items", strings.NewReader(`{"owner":"a"}`))
	createReq.Header.Set("Authorization", "Bearer "+tokenA)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("Create status = %d", createRec.Code)
	}
	var created itemResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/items/"+created.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+tokenB)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant Get status = %d, want 404", getRec.Code)
	}

	// tenant-b's own list stays empty.
	listReq := httptest.NewRequest(http.MethodGet, "/v1/items?limit=10", nil)
	listReq.Header.Set("Authorization", "Bearer "+tokenB)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	var list listResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("tenant-b list = %+v, want empty", list)
	}
}

// TestEndToEnd_MissingTenantIDRejectedBeforeNATS proves AC-2: a request
// with a valid token but no tenant_id claim is rejected with 403 before any
// NATS call is made.
func TestEndToEnd_MissingTenantIDRejectedBeforeNATS(t *testing.T) {
	router, priv := newTestRouter(t) // no tenants provisioned at all
	token := newSignedToken(t, priv, "")

	req := httptest.NewRequest(http.MethodGet, "/v1/items?limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestEndToEnd_TenantNotProvisioned(t *testing.T) {
	router, priv := newTestRouter(t) // no tenants provisioned
	token := newSignedToken(t, priv, "no-such-tenant")

	req := httptest.NewRequest(http.MethodGet, "/v1/items?limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

func TestEndToEnd_ListRequiresLimit(t *testing.T) {
	router, priv := newTestRouter(t, "acme-corp")
	token := newSignedToken(t, priv, "acme-corp")

	req := httptest.NewRequest(http.MethodGet, "/v1/items", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestEndToEnd_HealthEndpointsUnauthenticated(t *testing.T) {
	router, _ := newTestRouter(t)

	liveReq := httptest.NewRequest(http.MethodGet, "/healthz/live", nil)
	liveRec := httptest.NewRecorder()
	router.ServeHTTP(liveRec, liveReq)
	if liveRec.Code != http.StatusOK {
		t.Fatalf("live status = %d, want 200", liveRec.Code)
	}

	readyReq := httptest.NewRequest(http.MethodGet, "/healthz/ready", nil)
	readyRec := httptest.NewRecorder()
	router.ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", readyRec.Code)
	}
}
