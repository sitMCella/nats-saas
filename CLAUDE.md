# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repo state

This repo is currently **design-only** — seven design documents under `docs/`, no application source, no Terraform/Helm/Go code yet, no build system. There is nothing to build, lint, or test today. When code lands, update this file with the real commands.

## What this system is

A multi-tenant REST API (create/list/get/delete arbitrary JSON records per company) backed by NATS JetStream as the datastore, authenticated via a self-hosted Keycloak, all fronted by HAProxy Unified Gateway (HUG) in one AKS cluster, one Azure region. Auth0 was originally requested but has no self-hostable Helm chart, so Keycloak substitutes for it everywhere.

The core bet: NATS itself, not just API code, is the tenant-isolation boundary. Every tenant gets its own NATS **Account** (not a shared account with subject-prefixing) — the server refuses cross-account access outright, so a bug in tenant-resolution code fails closed (an error), never open (someone else's data).

## Document map — read the right one before touching a layer

| Doc | Owns | Read this before touching... |
|---|---|---|
| `docs/nats-tenant-queue-api/design.md` | Parent system design — REST API behavior, invariants, HTTP interface | anything cross-cutting; this is the system-level source of truth |
| `docs/terraform-infra/design.md` | Azure foundation: RGs, VNet, AKS, Key Vault, ACR, PostgreSQL Flexible Server | Terraform root module, Azure resources |
| `docs/nats-cluster/design.md` | NATS JetStream on AKS via Helm; Operator/SYS-account/resolver bootstrap | NATS chart, values, `nsc` bootstrap |
| `docs/keycloak-operator/design.md` | Keycloak on AKS via the official Operator, its DB, its Key Vault identity | Keycloak chart/CR, DB TLS trust |
| `docs/haproxy-unified-gateway/design.md` | HUG via Helm: `GatewayClass`, `Gateway`, static-IP binding | ingress/gateway config |
| `docs/nats-auth-middleware/design.md` | The REST API's Go middleware (AuthN, tenant NATS pool, JetStream KV) + image build/publish | the Go middleware, Dockerfile |
| `docs/rest-api-workload/design.md` | REST API's K8s Deployment/Service/Route, its Key Vault identity | REST API Helm chart / K8s manifests |
| `docs/tenant-provisioning/design.md` | One-time Keycloak realm setup, per-tenant onboarding script | `onboard-tenant.sh`, realm/client setup |
| `docs/nats-concepts-guide.md` | Explanatory (non-authoritative) NATS primer | orienting on NATS terms before design docs |
| `docs/keycloak-authentication-guide.md` | Explanatory (non-authoritative) Keycloak primer | orienting on Keycloak terms before design docs |
| `docs/implementation-plan.md` | Build sequencing across all seven design docs, with a dependency graph and two documented corrections to earlier docs | before starting any implementation phase |

Where a summary here and a design doc disagree, **the design doc wins**. Where `docs/implementation-plan.md` and an individual design doc disagree, the individual design doc is canonical (the plan only sequences work, per its own header).

## Non-negotiable invariants (apply across every layer)

- `tenant_id` is only ever trusted from a cryptographically verified Keycloak access token claim — never from a header, path segment, or any client-supplied value. Extracted once, in `AuthMiddleware`, carried only on `context.Context`.
- Every tenant's NATS Account, JetStream KV bucket, Keycloak `tenant_id` user attribute, and Key Vault secret name component use the **same byte-for-byte `tenant_id` string**, validated once against `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`.
- A NATS connection pool is keyed strictly by `tenant_id`; a connection for tenant X is never returned to a request verified as tenant Y, even under a concurrent-access race.
- No static credentials anywhere: Azure access is Workload Identity (federated OIDC) end to end; NATS Operator signing keys and per-tenant JWTs/seeds live only on the operator's workstation or in Key Vault, never in a manifest, image layer, or the cluster's etcd.
- Fail closed: a tenant with no matching Key Vault entry gets `503 tenant_not_provisioned`, never a fallback account or bucket created on the fly. A missing `tenant_id` claim gets `403` before any NATS or Key Vault call.
- Every write (`Create`, `Delete`) blocks on JetStream acknowledgment before the API reports success. `List` is always paginated (cursor + limit required, not optional) — no endpoint returns an unbounded result.
- Item IDs are server-generated ULIDs; the API never accepts a client-supplied item ID for create.
- Container images: multi-stage build, distroless non-root final stage (UID 65532), base images pinned by digest, tagged with the immutable short git commit SHA — never `latest`.
- POC scope: no TLS anywhere yet (Gateway listener, NATS protocol ports, Keycloak's own HTTP listener). This is deliberate and cross-cutting — don't add TLS to one layer without revisiting all of them together.

## Architecture at a glance

```
Internet → Azure Standard LB → HUG (Gateway + HTTPRoutes) → {REST API pods, Keycloak pods}
REST API pods: AuthMiddleware (verify JWT vs Keycloak JWKS) → Pool (per-tenant NATS conn, keyed by tenant_id)
                                                             → KVStore (JetStream KV Create/List/Get/Delete)
Pool fetches tenant NATS creds (JWT+seed) from Azure Key Vault via Workload Identity, cache miss only
Keycloak → Azure Database for PostgreSQL Flexible Server (its own state only)
NATS cluster: Operator → SYS account + one Account per tenant → one JetStream KV bucket per tenant → Azure Managed Disks
```

Key structural points, each easy to miss without reading the design docs:

- **A route lives with its backend Service's chart, not the Gateway's chart.** `keycloak-route` ships in `charts/keycloak-app`; `api-route` ships in `charts/rest-api-app`. `charts/hug-app` owns only `GatewayClass`/`Gateway`. Don't add routes to the HUG chart.
- **The REST API is the only component that ever holds a NATS credential.** Users and every other in-cluster workload are network-policy-blocked from reaching NATS directly; the only path is through the REST API.
- **`Pool` never queries the NATS resolver directly** — it only resolves credentials through Key Vault. A NATS Account/bucket that exists with no matching Key Vault entry is inert by design (fails closed).
- **The NATS Operator's private signing key never enters the cluster.** All bootstrap and per-tenant `nsc` operations run from an operator's own workstation; only public JWTs are pasted into Helm values or pushed to the resolver.
- **One shared Keycloak realm (`natssaas`), not one realm per tenant.** Tenant separation is a `tenant_id` user attribute + one protocol mapper on the `rest-api` client's `tenant` client scope — not realm boundaries.
- **Terraform module has one documented, already-corrected defect**: an earlier revision granted `Key Vault Secrets User` to the AKS kubelet identity (which has no federated credential a pod's ServiceAccount can exchange a token against). The fix — dedicated `id-rest-api` and `id-keycloak` managed identities with their own federated credentials — is folded into the current design (`docs/rest-api-workload/design.md` §4, `docs/keycloak-operator/design.md` §4.1). Apply phase 1 and phase 2 of the implementation plan as one `terraform apply`, not two milestones.
- **Cross-tenant reads/deletes return `404`, never `403`.** An item ID that exists only in another tenant's bucket is indistinguishable from an ID that doesn't exist at all.

## Go middleware shape (once code exists)

The design docs specify these interfaces for `docs/nats-auth-middleware/design.md`'s package — implement against this shape, don't invent a different one without updating the design doc first:

```go
func AuthMiddleware(jwks JWKSClient) func(next http.Handler) http.Handler
func TenantIDFromContext(ctx context.Context) (string, error)
func SubjectFromContext(ctx context.Context) (string, error)

type Pool interface {
    Get(ctx context.Context) (*nats.Conn, error) // ErrTenantNotProvisioned on Key Vault miss
}

type KVStore interface {
    Create(ctx context.Context, payload json.RawMessage) (Item, error)
    List(ctx context.Context, cursor string, limit int) (items []ItemSummary, nextCursor string, err error)
    Get(ctx context.Context, id string) (Item, error)
    Delete(ctx context.Context, id string) error
}
```

`sub` (user identity) is available for audit logging only — it never selects a NATS connection or bucket; isolation is tenant-level, not user-level (`INV-4`). Health endpoints (`/healthz/live`, `/healthz/ready`) are intentionally unauthenticated and never check NATS/Key Vault reachability — only the JWKS cache's readiness.

## Naming rule shared across three systems

`tenant_id` (`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`) is chosen once by an operator at onboarding time — never by the tenant, never editable afterward — and reused identically as: the Keycloak user's `tenant_id` attribute, the NATS Account name, and the Key Vault secret name component (`tenant-<tenant_id>-nats-jwt` / `tenant-<tenant_id>-nats-seed`). Re-running `./onboard-tenant.sh <tenant_id> <admin_email>` for an existing tenant is a required safe no-op, not an error — this is how partial-failure onboarding resumes.
