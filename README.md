# nats-saas

`nats-saas` is a multi-tenant SaaS backend: each tenant (company) gets a
REST API to create, list, get, and delete arbitrary JSON records, with hard
isolation from every other tenant's data. Instead of a traditional
database, the datastore is NATS JetStream's key-value store — one bucket
per tenant. Requests are authenticated via a self-hosted Keycloak, 
and everything is fronted by HAProxy Unified Gateway (HUG). 
All Kubernetes resources are deployed in a single AKS cluster, and all 
Azure resources reside in a single Azure region.

```
Internet → Azure Standard LB → HUG (Gateway + HTTPRoutes) → REST API pods / Keycloak pods
REST API pods: verify JWT against Keycloak JWKS → pick per-tenant NATS
connection from a pool keyed by tenant_id → JetStream KV Create/List/Get/Delete
NATS cluster: one Account per tenant → one JetStream KV bucket per tenant
```

The central design decision: tenant isolation is enforced by NATS itself,
not just by API code. Every tenant gets its own NATS Account — not a
shared account with subject-prefixing — so the NATS server refuses
cross-account access outright. That means a bug in the API's
tenant-resolution logic fails closed (an error), never open (leaking
another tenant's data).

## Scope

This is a POC. No TLS anywhere yet (Gateway listener, NATS protocol ports,
Keycloak's own HTTP listener) — deliberate, cross-cutting tradeoff. The Azure
access is Workload Identity end to end, no static credentials anywhere.

Repository layout:

- `docs/` — design docs, one per layer (system, Terraform, NATS cluster,
  Keycloak, HUG, Go middleware, REST API workload, tenant onboarding), plus
  an implementation plan sequencing the build across all of them
- `terraform/` — Azure foundation (Resource Groups, VNet, AKS, Key Vault,
  ACR, PostgreSQL Flexible Server)
- `helm/` — Helm charts and helmfile for NATS, Keycloak, HUG, REST API
- `nats-auth-middleware/` — Go REST API / middleware (AuthN against
  Keycloak JWKS, per-tenant NATS connection pool, JetStream KV store)
- `onboard-tenant.sh` — one-time per-tenant onboarding (Keycloak user
  attribute, NATS Account, Key Vault secrets)

Read `CLAUDE.md` for the non-negotiable invariants (tenant_id handling,
fail-closed behavior, credential handling) before touching any layer.

## Deployment

Full manual configuration and deployment steps for Azure — Terraform
bootstrap, NATS/Keycloak/HUG/REST API Helm install, tenant onboarding — are
in [`manual-deployment.md`](manual-deployment.md).
