# See docs/terraform-infra/design.md §6 (Example Terraform configuration) for the
# canonical shape this file follows. Provider/Terraform versions here are pinned
# per explicit project decision, not the design doc's own (looser) defaults.

terraform {
  required_version = "= 1.15.8"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "= 5.2.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }

  # One-time manual bootstrap creates this Storage Account and container before
  # the first `terraform init` — see docs/terraform-infra/design.md §6, "State
  # backend bootstrap". Fill in the real backend before running `terraform init`.
  backend "azurerm" {
    resource_group_name  = "rg-natssaas-tfstate"
    storage_account_name = "stnatssaastfstate"
    container_name       = "tfstate"
    key                  = "prod/terraform.tfstate"
  }
}
