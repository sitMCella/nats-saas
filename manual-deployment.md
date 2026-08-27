# Manual Deployment

## Azure infrastructure (Terraform)

The Terraform project under `terraform/` provisions the Azure foundation described in
`docs/terraform-infra/design.md`: Resource Groups, a VNet, an AKS cluster,
Key Vault, Container Registry, and a PostgreSQL Flexible Server.

### Prerequisites

- Terraform `1.15.8` (pinned in `terraform/versions.tf`)
- Azure CLI, logged in with `az login` and the target subscription selected:
  `az account set --subscription <subscription-id>`
- Contributor (or Owner) on the subscription — this project creates three
  Resource Groups and everything inside them
- `Key Vault Secrets Officer` on the subscription —
  Contributor alone does not grant Key Vault data-plane access; Terraform
  reads/writes secrets directly (e.g. checks for `postgres-admin-password`)
- The Terraform state backend already bootstrapped (one-time, outside
  Terraform — see below); `terraform/versions.tf`'s `backend "azurerm"` block
  points at it
- `terraform/variables.tfvars` filled in, at minimum `tenant_id` and
  `aks_kubernetes_version` (no defaults — see `terraform/variables.tf`)

### One-time: bootstrap the state backend

Terraform cannot create the backend it stores its own state in
(`docs/terraform-infra/design.md` §6, "State backend bootstrap"). Create it by
hand once, matching the names in `terraform/versions.tf`:

```bash
az group create --name rg-natssaas-tfstate --location westeurope
az storage account create \
  --name stnatssaastfstate \
  --resource-group rg-natssaas-tfstate \
  --sku Standard_LRS \
  --allow-blob-public-access false
az storage container create \
  --name tfstate \
  --account-name stnatssaastfstate \
  --auth-mode login
```

### Create the infrastructure

```bash
cd terraform
terraform init
terraform plan -var-file=variables.tfvars
terraform apply -var-file=variables.tfvars
```

`terraform apply` runs a single dependency graph — Resource Groups, VNet,
Key Vault, AKS, Container Registry, role assignments, PostgreSQL — in the
order Terraform resolves from resource references, no `-target` or two-phase
apply needed. Re-running `terraform plan` afterward with no code change
should show zero planned changes.

### Tear down

```bash
terraform destroy -var-file=variables.tfvars
```

The Resource Groups, Key Vault, Container Registry, and PostgreSQL
server all carry `prevent_destroy` (`INV-4`); `destroy` fails on them until
that guard is removed from code, by design.

## Gateway API CRDs

Connect to the AKS cluster and install the Gateway API CRDs.

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
```

## NATS cluster (Helm)

`helm/helmfile.yaml` + `helm/values-nats-aks.yaml` install the
clustered, JetStream-enabled `nats/nats` chart described in
`docs/nats-cluster/design.md`, into the AKS cluster the Terraform project
above provisions. Requires phase 1+2 (AKS cluster reachable via `kubectl`)
already applied.

### Prerequisites

- Helm `3.7+`, [Helmfile](https://github.com/helmfile/helmfile) `0.150+` with
  the `helm-diff` plugin
- [`nsc`](https://github.com/nats-io/nsc) `v2.15.0` and the
  [NATS CLI](https://github.com/nats-io/natscli) (`nats`) `v0.4.0`, on the
  operator's own workstation only — never in-cluster
- `kubectl` context pointed at the AKS cluster from the Terraform project

### One-time: bootstrap the Operator, SYS account, and JWT resolver

Run on the operator's own workstation (`docs/nats-cluster/design.md` §4).
This is the one credential in the whole system that can mint a new, trusted
NATS Account from nothing — its signing keys (`~/.nsc`) never enter the
cluster and are not part of the commands below:

```bash
nsc add operator --generate-signing-key natssaas
nsc add account SYS
nsc edit operator --system-account SYS
nsc add user -a SYS sys
nsc generate config --nats-resolver --sys-account SYS > resolver.conf
```

Paste `resolver.conf`'s `operator`, `system_account`, `resolver`, and
`resolver_preload` values into the four `REPLACE_ME_*` placeholders in
`helm/values-nats-aks.yaml`'s `config.merge` block (the Operator JWT and `SYS`
account JWT/pubkey are public and self-verifying — safe to commit; the
Operator's own signing keys are not, and are never part of this file).
Also replace `config.cluster.routeURLs.password` with a generated secret,
stored like any other credential.

### Install the chart

```bash
cd helm
helmfile diff
helmfile apply

kubectl get pods -n nats -l app.kubernetes.io/instance=nats
kubectl get pvc -n nats
kubectl exec -n nats nats-box-<pod-suffix> -- nats server list --context default
```

A healthy cluster shows the `nats-N` pods `Running`/`1/1 Ready`, `nats
server list` reporting two cluster members, and four bound PVCs (two
`*-js`, two `*-resolver`).

No tenant Account exists yet after this step — that is
`docs/tenant-provisioning/design.md`'s `onboard-tenant.sh`, a later phase.

### Tear down

```bash
cd helm
helmfile destroy
kubectl get pvc -n nats     # PVCs survive by default
kubectl delete pvc -n nats -l app.kubernetes.io/instance=nats   # data-destroying, opt-in
```

## Keycloak (Operator)

`helm/charts/keycloak-app` + `helm/values-keycloak-aks.yaml.gotmpl` deploy Keycloak
via the official Keycloak Operator, per `docs/keycloak-operator/design.md`. Requires
phase 1+2 (AKS, `id-keycloak` identity, CSI Secrets Store addon) already applied.

### Prerequisites

- `kubectl` context pointed at the AKS cluster
- Helm `3.7+`, Helmfile `0.150+`
- `psql` (or `az postgres flexible-server` connect helpers), for the one manual
  database step below
- `openssl`, `curl` — for assembling the Postgres CA trust bundle

### One-time: create Keycloak's database and role

Terraform (phase 2) already generated the `keycloak` role's password and stored it
in Key Vault as `keycloak-db-password` — it does not create the role itself, since
Terraform never opens a connection to PostgreSQL. From a host with network access to
the Flexible Server (in-VNet, or a workstation temporarily added to
`postgres_firewall_allowed_cidrs`):

```bash
az keyvault secret show --vault-name <key_vault_name> --name postgres-admin-password --query value -o tsv
az keyvault secret show --vault-name <key_vault_name> --name keycloak-db-password --query value -o tsv

psql "host=<postgres_fqdn> port=5432 dbname=postgres user=<postgres_admin_username> sslmode=verify-full"
```

```sql
CREATE ROLE keycloak WITH LOGIN PASSWORD '<value of keycloak-db-password above>';
CREATE DATABASE keycloak OWNER keycloak;
\c keycloak
ALTER SCHEMA public OWNER TO keycloak;
```

The `keycloak` role must have `LOGIN` permission and own both the `keycloak`
database and its `public` schema — Keycloak's own migrations create/alter
objects in `public` at startup and fail without schema ownership.

The role's password must match the `keycloak-db-password` secret exactly — this
step only makes PostgreSQL match what Terraform already put in Key Vault.

### One-time: assemble the Postgres CA trust bundle

Azure Database for PostgreSQL's certificate chain is mid-rotation, so both current
root CAs are needed together — never an intermediate or server certificate:

```bash
cd helm
curl -sSL -o /tmp/digicert-global-root-g2.crt https://cacerts.digicert.com/DigiCertGlobalRootG2.crt
curl -sSL -o /tmp/ms-rsa-root-2017.crt "https://www.microsoft.com/pkiops/certs/Microsoft%20RSA%20Root%20Certificate%20Authority%202017.crt"
openssl x509 -inform der -in /tmp/digicert-global-root-g2.crt -out /tmp/digicert-global-root-g2.pem
openssl x509 -inform der -in /tmp/ms-rsa-root-2017.crt -out /tmp/ms-rsa-root-2017.pem
cat /tmp/digicert-global-root-g2.pem /tmp/ms-rsa-root-2017.pem > azure-postgres-ca.pem
```

`azure-postgres-ca.pem` must sit next to `helmfile.yaml` (`helm/`) — the `.gotmpl`
values file reads it in via `readFile`.

### One-time: install the Keycloak Operator

Namespace-scoped, least privilege — the Operator only ever watches its own
namespace here:

```bash
kubectl create namespace keycloak
kubectl apply -k 'github.com/keycloak/keycloak-k8s-resources/kubernetes?ref=26.7.2'

kubectl get pods -n keycloak -l app.kubernetes.io/name=keycloak-operator
kubectl get crds | grep k8s.keycloak.org
```

This installs the `keycloaks.k8s.keycloak.org` / `keycloakrealmimports.k8s.keycloak.org`
CRDs and the Operator's own controller — the app chart below only ever succeeds
after this step, since it creates instances of these CRDs, not the CRDs themselves.

### Fill in the values file

Replace the `REPLACE_ME_*` placeholders in `values-keycloak-aks.yaml.gotmpl` with
the `keycloak_identity_client_id`, `key_vault_name`, `postgres_fqdn` Terraform
outputs and the Azure tenant ID.

### Install the chart

```bash
cd helm
helmfile diff
helmfile apply

kubectl get keycloaks/keycloak -n keycloak -o go-template='{{range .status.conditions}}{{.type}}: {{.status}} — {{.message}}{{"\n"}}{{end}}'
kubectl get pods -n keycloak -l app=keycloak
kubectl get svc keycloak-service -n keycloak
kubectl get httproute keycloak-route -n keycloak -o yaml   # ResolvedRefs: True once the HUG Gateway exists
```

A healthy deployment reports `Ready: True`, `HasErrors: False`. `keycloak-route`
stays unresolved until the HUG `Gateway`/`GatewayClass` (a later phase) exists —
this is expected, not a failure of this step.

### Tear down

```bash
cd helm
helmfile destroy   # removes ServiceAccount, SecretProviderClass, CA Secret, Keycloak CR, HTTPRoute

kubectl delete -k 'github.com/keycloak/keycloak-k8s-resources/kubernetes?ref=26.7.2'
kubectl delete namespace keycloak
```

This never removes the PostgreSQL database/role (`DROP DATABASE keycloak; DROP ROLE keycloak;`
by hand) or any Terraform-managed Azure resource — neither has a representation in
this Helm release.

## HAProxy Unified Gateway (HUG)

`helm/values-hug-aks.yaml` (upstream chart) + `helm/charts/hug-app`
(`GatewayClass`/`Gateway`) install HUG per
`docs/haproxy-unified-gateway/design.md`, fronting Keycloak and the REST API
behind the AKS cluster's static public IP. Requires phase 1 (AKS, static
public IP `pip-natssaas-prod`) already applied.

### Prerequisites

- `kubectl` context pointed at the AKS cluster, cluster-admin access (the
  chart's hooks install CRDs cluster-wide; `hug-app` creates a cluster-scoped
  `GatewayClass`)
- Helm `3.7+`, Helmfile `0.150+` with the `helm-diff` plugin

### Install

Both releases (`hug`, `hug-app`) are already declared in `helm/helmfile.yaml`,
`hug-app` waiting on `hug`'s `gwapijob` hook via `needs:`:

```bash
cd helm
helmfile diff
helmfile apply

kubectl get pods -n haproxy-unified-gateway -l "app.kubernetes.io/name=haproxy-unified-gateway,app.kubernetes.io/instance=hug"
kubectl get jobs -n haproxy-unified-gateway
kubectl get crds | grep -E 'gateway.networking.k8s.io|gate.v3.haproxy.org'
kubectl get gateway hug-gateway -n haproxy-unified-gateway -o yaml   # Programmed: True
kubectl get svc hug-haproxy-unified-gateway -n haproxy-unified-gateway -w
```

`hug`'s `Service` starts with only its `stat`/`controller-metrics` ports;
`http`/`https` ports are added automatically once the `Gateway` (from
`hug-app`) exists. Watching `-w` shows the Azure Load Balancer's
`EXTERNAL-IP` settle to the pinned static IP within a few minutes.

`keycloak-route` and `api-route` are **not** part of this chart — they ship
with their own backend Service's chart (`keycloak-app`, `rest-api-app`) and
apply from those documents' own `helmfile apply`. Once all three releases are
applied:

```bash
kubectl get httproute -A   # api-route and keycloak-route, both ResolvedRefs: True
```

### Tear down

```bash
cd helm
helmfile destroy   # tears down both hug-app (GatewayClass, Gateway) and hug
```

Does not remove the CRDs the `hug` release's hook Jobs installed, or the
Azure static public IP — that is Terraform-managed state, untouched by this
Helm release.

## REST API image (nats-auth-middleware)

`nats-auth-middleware/` is the Go source for the REST API's AuthN/AuthZ
middleware, per-tenant NATS connection pool, and JetStream KV operations,
per `docs/nats-auth-middleware/design.md`. This is phase 6: it only needs
phase 1 (the ACR from the Terraform project above) and does not depend on
phases 3-5 — it can build in parallel with the NATS/Keycloak/HUG installs.

### Prerequisites

- Go `1.25+` (`nats-auth-middleware/go.mod`)
- Docker, for the image build
- Azure CLI (`az`), for `az acr login`

### Run tests

```bash
cd nats-auth-middleware
go build ./...
go vet ./...
go test ./... -race
```

Every package below `internal/` has real test coverage: `authmw` signs and
verifies tokens against an in-memory JWKS set; `pool` and `kvstore` boot a
real in-process, operator-mode `nats-server` (`internal/testnats`) with
minted per-tenant Account/User JWTs, the same JWT+seed auth model production
uses, and prove tenant isolation and concurrent-cache-miss collapsing under
`-race` (`INV-3`, `AC-3`, `AC-4`); `httpapi` drives the full stack —
`AuthMiddleware` + `Pool` + `KVStore` — through real HTTP requests and
proves cross-tenant reads 404, never 403 or 200 (`AC-1`, `AC-2`, `AC-3` from
`docs/nats-tenant-queue-api/design.md`).

### Build and push the image

```bash
cd nats-auth-middleware
az acr login --name <acr_name>   # exchanges the Azure AD token for registry auth, no password

IMAGE="<acr_login_server>/nats-auth-middleware:$(git rev-parse --short HEAD)"
docker build --platform linux/amd64 -t "$IMAGE" .
docker push "$IMAGE"
```

`<acr_login_server>` is the `acr_login_server` output from the Terraform
project above. The image is always tagged with the immutable short git
commit SHA — never `latest` — and only pushes successfully when the
runner's source IP is on `acr_firewall_allowed_cidrs` and its token carries
`AcrPush` (`docs/terraform-infra/design.md` `INV-9`, `INV-11`).

### Verify the image (`AC-6`)

```bash
docker inspect <image> --format '{{.Config.User}}'        # nonroot:nonroot
docker inspect <image> --format '{{json .Config.Entrypoint}}'   # ["/nats-auth-middleware"], no shell
```

### Runtime configuration

The binary (`cmd/nats-auth-middleware`) reads everything from environment
variables (`internal/config`) — no flags, no config file:

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `LISTEN_ADDR` | no | `:8080` | HTTP listen address |
| `KEYCLOAK_JWKS_URL` | yes | — | Keycloak realm's JWKS endpoint |
| `KEYCLOAK_ISSUER` | yes | — | expected token `iss` claim |
| `KEYCLOAK_AUDIENCE` | no | — | expected token `aud` claim; empty skips the check |
| `NATS_URL` | yes | — | NATS cluster's in-cluster address |
| `KEY_VAULT_URL` | yes | — | Key Vault URI (`key_vault_uri` Terraform output) |
| `POOL_TTL` | no | `15m` | per-tenant NATS connection cache TTL |
| `POOL_MAX_ENTRIES` | no | `500` | max cached NATS connections |
| `SHUTDOWN_GRACE` | no | `10s` | drain window on `SIGTERM`/`SIGINT` |

Azure Key Vault access uses `azidentity.NewWorkloadIdentityCredential` —
no static Azure credential anywhere in this process; this only resolves
inside a pod carrying the `id-rest-api` Workload Identity annotations
(`docs/rest-api-workload/design.md`, a later phase).

Deploying this image into the cluster (a Kubernetes Deployment referencing
the pushed tag) is `docs/rest-api-workload/design.md`, a separate,
out-of-scope step from this one.

## REST API workload (Kubernetes)

`helm/charts/rest-api-app` + `helm/values-rest-api-aks.yaml` deploy the
pushed `nats-auth-middleware` image as a running, HUG-reachable workload —
`nats-saas-api` namespace, ServiceAccount, ConfigMap, two-replica
Deployment, Service, `api-route` `HTTPRoute`, `rest-api-allow-hug`
`NetworkPolicy`, `PodDisruptionBudget` — per
`docs/rest-api-workload/design.md`. This is phase 7: it needs phase 2 (the
`id-rest-api` identity, already part of the Terraform project above), phase
3 (NATS reachable), phase 4 (Keycloak reachable, for JWKS), phase 5 (the
`hug-gateway` `Gateway` to attach to), and phase 6 (a pushed image tag).

### Prerequisites

- `kubectl` context pointed at the AKS cluster
- Helm `3.7+`, Helmfile `0.150+` with the `helm-diff` plugin
- Phases 2–6 above already applied

### Fill in the values file

Replace the `REPLACE_ME_*` placeholders in `helm/values-rest-api-aks.yaml`:

| Placeholder | Value |
|---|---|
| `REPLACE_ME_REST_API_IDENTITY_CLIENT_ID` | `rest_api_identity_client_id` Terraform output |
| `REPLACE_ME_ACR_LOGIN_SERVER` | `acr_login_server` Terraform output |
| `REPLACE_ME_GIT_SHA` | the short git commit SHA tag pushed in the REST API image step above |

### Install the chart

```bash
cd helm
helmfile diff
helmfile apply

kubectl get pods -n nats-saas-api -l app=rest-api
kubectl get svc rest-api -n nats-saas-api
kubectl get httproute api-route -n nats-saas-api -o yaml   # ResolvedRefs: True
```

A healthy deployment shows two `Ready` `rest-api` pods and `api-route`
reporting `ResolvedRefs: True`. Until a tenant is onboarded (next section),
every request still fails with `503 tenant_not_provisioned` — `Pool.Get`
finds no Key Vault entry for any `tenant_id` yet, by design.

### Check the Application

```bash
# Get the IP Address
kubectl get svc -n haproxy-unified-gateway hug-haproxy-unified-gateway -o jsonpath='{.status.loadBalancer.ingress[0].ip}'

echo "<address> api.natssaas.example.com" | sudo tee -a /etc/hosts

curl -v http://api.natssaas.example.com/healthz/ready
```

### Tear down

```bash
cd helm
helmfile destroy   # removes ServiceAccount, ConfigMap, Deployment, Service, api-route, NetworkPolicy, PodDisruptionBudget
```

Does not remove the `id-rest-api` Terraform identity or any Azure resource.

## Configure Keycloak

### Access Keycloak

```bash
kubectl get svc/hug-haproxy-unified-gateway -n haproxy-unified-gateway # Retrieve the external IP address

echo "<address> auth.natssaas.example.com" | sudo tee -a /etc/hosts
```

Keycloak Web URI for natssaas realm: http://auth.natssaas.example.com/auth/realms/natssaas/account

### Configure Keycloak

#### Login in Keycloak as administrator

Keycloak custom administrator account credentials:
```sh
ADMIN_USER=$(az keyvault secret show --vault-name <key_vault_name> --name keycloak-admin-username --query value -o tsv)
ADMIN_PASSWORD=$(az keyvault secret show --vault-name <key_vault_name> --name keycloak-admin-password --query value -o tsv)
```

If the Keyclock custom administrator account has not been configured correctly, then retrieve the credentials for the temporary Keycloak account:
```sh
kubectl get secret keycloak-initial-admin -n keycloak -o go-template='
{{range $k,$v := .data}}{{printf "%s: " $k}}{{if not $v}}{{$v}}{{else}}{{$v | base64decode}}{{end}}{{"\n"}}{{end}}'
```

#### Enable the Unmanaged Attributes in the Keycloak realm, required for setting the user attributes.

From Keycloak natssaas realm access Realm Settings → General → Unmanaged Attributes = Enabled

#### Configure the Clients Settings

From Keycloak natssaas realm, open the page Clients > Settings. Enable "Client authentication" and "Direct access grants".

From Keycloak natssaas realm, open the page Clients > rest-api > Roles. Create a role "user".

## Tenant provisioning

Two parts, per `docs/tenant-provisioning/design.md`: a one-time Keycloak
realm/client/protocol-mapper setup, run once ever before the first tenant,
and `onboard-tenant.sh`, run once per tenant thereafter. This is phase 8:
it needs phase 4 (Keycloak reachable) and phase 3 (NATS resolver reachable)
already applied.

### Prerequisites

- `kcadm.sh` (Keycloak's admin CLI) Installed in the Operator machine.
- `kcadm.sh` (Keycloak's admin CLI), authenticated against the deployed
  Keycloak instance
- `nsc` and the NATS CLI (`nats`), on the same operator workstation that
  holds the Operator's signing keys from the NATS cluster bootstrap above
- Azure CLI (`az`), with an identity granted `Key Vault Secrets Officer`
  (write) on the Key Vault — see `docs/tenant-provisioning/design.md` §8

### Connect to Keycloak and NATS

```bash
## Connect to Keycloak
kubectl port-forward -n keycloak svc/keycloak-service 8080:8080

## Connect to NATS
kubectl port-forward -n nats svc/nats 4222:4222
```

### One-time: Keycloak realm, client, and protocol mapper

Run once, ever, before onboarding the first tenant (`docs/tenant-provisioning/design.md`
§4.1). Getting this step wrong affects every tenant's tokens at once, so
review it carefully before moving on:

```bash
# Keycloak custom administrator account credentials:
ADMIN_USER=$(az keyvault secret show --vault-name <key_vault_name> --name keycloak-admin-username --query value -o tsv)
ADMIN_PASSWORD=$(az keyvault secret show --vault-name <key_vault_name> --name keycloak-admin-password --query value -o tsv)

# If the Keyclock custom administrator account has not been configured correctly, then retrieve the credentials for the temporary Keycloak account:
ADMIN_USER=$(kubectl get secret keycloak-initial-admin -n keycloak -o jsonpath='{.data.username}' | base64 -d)
ADMIN_PASSWORD=$(kubectl get secret keycloak-initial-admin -n keycloak -o jsonpath='{.data.password}' | base64 -d)

kcadm.sh config credentials --server http://auth.natssaas.example.com/auth --realm master \
  --user "${ADMIN_USER}" --password "${ADMIN_PASSWORD}"

kcadm.sh create realms -s realm=natssaas -s enabled=true

REST_API_CLIENT_ID=$(kcadm.sh create clients -r natssaas \
  -s clientId=rest-api -s publicClient=false -s enabled=true -i)

TENANT_SCOPE_ID=$(kcadm.sh create client-scopes -r natssaas \
  -s name=tenant -s protocol=openid-connect -i)

kcadm.sh create "client-scopes/${TENANT_SCOPE_ID}/protocol-mappers/models" -r natssaas \
  -s name=tenant_id \
  -s protocol=openid-connect \
  -s protocolMapper=oidc-usermodel-attribute-mapper \
  -s 'config."user.attribute"=tenant_id' \
  -s 'config."claim.name"=tenant_id' \
  -s 'config."jsonType.label"=String' \
  -s 'config."id.token.claim"=true' \
  -s 'config."access.token.claim"=true'

kcadm.sh update "clients/${REST_API_CLIENT_ID}/default-client-scopes/${TENANT_SCOPE_ID}" -r natssaas
```

Verify: a decoded access token for any user with a `tenant_id` attribute
carries a `tenant_id` claim.

### Restart nats-saas-api

```bash
kubectl rollout restart deployment/rest-api -n nats-saas-api
```

### Per-tenant: run the onboarding script

```bash
export KEYCLOAK_URL=https://auth.natssaas.example.com/auth
export KEYCLOAK_ADMIN_USER=$(az keyvault secret show --vault-name <key_vault_name> --name keycloak-admin-username --query value -o tsv)
export KEYCLOAK_ADMIN_PASSWORD=$(az keyvault secret show --vault-name <key_vault_name> --name keycloak-admin-password --query value -o tsv)
export NATS_URL=nats://localhost:4222
export KEY_VAULT_NAME=<key_vault_name>    # Terraform output

# Onboard two tenants
nsc push -a tenant-a -u nats://localhost:4222 --system-account SYS --system-user sys
./onboard-tenant.sh tenant-a admin@tenant-a.example

nsc push -a tenant-b -u nats://localhost:4222 --system-account SYS --system-user sys
./onboard-tenant.sh tenant-b admin@tenant-b.example
```

Prints a `[n/5]` status line per step. Re-running with the same `tenant_id`
is a safe no-op for whichever steps already succeeded — this is how a
partial failure resumes; see the script's own header comment for the full
per-step failure/idempotency behavior.

### Keycloak User Account

Each tenant's user resets their temporary password, logs in, and gets a token.

```bash
# Reset the temporary password for the tenant's user
kubectl exec -n keycloak keycloak-0 -- /opt/keycloak/bin/kcadm.sh set-password \
  --server http://localhost:8080/auth --realm master --user "${ADMIN_USER}" \
  --password "${ADMIN_PASSWORD}" -r natssaas \
  --username admin@tenant-a.example --new-password <admin-tenant-a-password>

  kubectl exec -n keycloak keycloak-0 -- /opt/keycloak/bin/kcadm.sh set-password \
    --server http://localhost:8080/auth --realm master --user "${ADMIN_USER}" \
    --password "${ADMIN_PASSWORD}" -r natssaas \
    --username admin@tenant-b.example --new-password <admin-tenant-b-password>
```

Each tenant's user login in Keycloak and complete the configuration.
Keycloak Web URI: http://auth.natssaas.example.com/auth/realms/natssaas/account

Access Keycloak as administrator. From the natssaas realm, access the page Users > admin@tenant-a.example > Role mapping > Assign role > Client roles > user.
From the natssaas realm, access the page Users > admin@tenant-b.example > Role mapping > Assign role > Client roles > user.

### Keycloak JWT Tokens

Generate the TOKEN_A / TOKEN_B set to each tenant's access token:

```bash
export CLIENT_SECRET=$(kcadm.sh get "clients/${REST_API_CLIENT_ID}/client-secret" -r natssaas --fields value --format csv --noquotes)

TOKEN_A=$(curl -s -X POST http://auth.natssaas.example.com/auth/realms/natssaas/protocol/openid-connect/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=rest-api" \
  -d "client_secret=${CLIENT_SECRET}" \
  -d "grant_type=password" \
  -d "username=admin@tenant-a.example" \
  -d "password=<admin-tenant-a-password>" | jq -r .access_token)

# Verify that the account is active
curl -s \
-X POST \
http://auth.natssaas.example.com/auth/realms/natssaas/protocol/openid-connect/token/introspect \
-d "client_id=rest-api" \
-d "client_secret=${CLIENT_SECRET}" \
-d "token=${TOKEN_A}" \
| jq

TOKEN_B=$(curl -s -X POST http://auth.natssaas.example.com/auth/realms/natssaas/protocol/openid-connect/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=rest-api" \
  -d "client_secret=${CLIENT_SECRET}" \
  -d "grant_type=password" \
  -d "username=admin@tenant-b.example" \
  -d "password=<admin-tenant-b-password>" | jq -r .access_token)

# Verify that the account is active
curl -s \
-X POST \
http://auth.natssaas.example.com/auth/realms/natssaas/protocol/openid-connect/token/introspect \
-d "client_id=rest-api" \
-d "client_secret=${CLIENT_SECRET}" \
-d "token=${TOKEN_B}" \
| jq
```

### Interact with the Application REST API

```bash
curl -s -X POST https://api.natssaas.example.com/v1/items \
  -H "Authorization: Bearer ${TOKEN_A}" -H 'Content-Type: application/json' \
  -d '{"foo":"bar"}'
# -> 201, item lands in tenant-a's own bucket

curl -s https://api.natssaas.example.com/v1/items/<item_id_from_tenant_a> \
  -H "Authorization: Bearer ${TOKEN_B}"
# -> 404, never 403 — tenant-b cannot see tenant-a's item

# A valid token missing the tenant_id claim (e.g. a user with no attribute set)
curl -s -o /dev/null -w '%{http_code}\n' https://api.natssaas.example.com/v1/items \
  -H "Authorization: Bearer ${TOKEN_NO_TENANT}"
# -> 403, before any NATS or Key Vault call

# Resilience: kill one pod of each component in turn, confirm the system
# keeps serving traffic through the remaining replica each time
kubectl delete pod -n nats-saas-api -l app=rest-api --field-selector status.phase=Running -o name | head -n1 | xargs kubectl delete
kubectl delete pod -n nats -l app.kubernetes.io/instance=nats -o name | head -n1 | xargs kubectl delete
kubectl delete pod -n keycloak -l app=keycloak -o name | head -n1 | xargs kubectl delete

# Drift check
cd terraform && terraform plan -var-file=variables.tfvars   # no changes
cd ../helm && helmfile diff                                 # no changes
```
