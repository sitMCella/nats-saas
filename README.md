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
