# nats-saas

## Azure infrastructure (Terraform)

Terraform project under `terraform/` provisions the Azure foundation described in
`docs/terraform-infra/design.md`: three Resource Groups, a VNet, an AKS cluster,
Key Vault, Container Registry, and a PostgreSQL Flexible Server.

### Prerequisites

- Terraform `1.5.7` (pinned in `terraform/versions.tf`)
- Azure CLI, logged in with `az login` and the target subscription selected:
  `az account set --subscription <subscription-id>`
- Contributor (or Owner) on the subscription — this project creates three
  Resource Groups and everything inside them
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

The three Resource Groups, Key Vault, Container Registry, and PostgreSQL
server all carry `prevent_destroy` (`INV-4`); `destroy` fails on them until
that guard is removed from code, by design.

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
nsc generate config --nats-resolver --sys-account SYS -o resolver.conf
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

A healthy cluster shows three `nats-N` pods `Running`/`1/1 Ready`, `nats
server list` reporting three cluster members, and six bound PVCs (three
`*-js`, three `*-resolver`).

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
```

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

Never removes the PostgreSQL database/role (`DROP DATABASE keycloak; DROP ROLE keycloak;`
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
