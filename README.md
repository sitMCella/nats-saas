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
