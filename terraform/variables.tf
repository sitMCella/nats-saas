# Root module variables — docs/terraform-infra/design.md §6 (Key input variables)
# and §5 (Requirements) for defaults tied to invariants (fail-closed firewalls).

variable "location" {
  description = "Azure region for every resource in this project."
  type        = string
  default     = "westeurope"
}

variable "env" {
  description = "Environment suffix used in resource names. Only \"prod\" exists today."
  type        = string
  default     = "prod"
}

variable "tenant_id" {
  description = "Azure AD tenant ID."
  type        = string
}

variable "subscription_id" {
  description = "Azure subscription ID every resource in this project deploys into."
  type        = string
}

variable "network_resource_group_name" {
  type    = string
  default = "rg-natssaas-network-prod"
}

variable "platform_resource_group_name" {
  type    = string
  default = "rg-natssaas-platform-prod"
}

variable "data_resource_group_name" {
  type    = string
  default = "rg-natssaas-data-prod"
}

variable "vnet_address_space" {
  type    = string
  default = "10.60.0.0/16"
}

variable "aks_subnet_cidr" {
  description = "snet-aks — AKS node NICs (pod IPs are overlay, not VNet-routed)."
  type        = string
  default     = "10.60.0.0/22"
}

variable "pe_subnet_cidr" {
  description = "snet-pe — private endpoint NICs (Key Vault, PostgreSQL, ACR)."
  type        = string
  default     = "10.60.5.0/24"
}

variable "aks_kubernetes_version" {
  type = string
}

variable "aks_system_node_vm_size" {
  type    = string
  default = "Standard_D2s_v5"
}

variable "aks_system_node_min_count" {
  type    = number
  default = 1
}

variable "aks_system_node_max_count" {
  type    = number
  default = 3
}

variable "aks_api_server_authorized_ip_ranges" {
  description = "CIDR ranges admitted to the AKS API server. Empty leaves it open to the public internet; set to office/VPN egress and CI runner ranges before GA (design doc open questions)."
  type        = list(string)
  default     = []
}

variable "key_vault_allowed_public_ip" {
  description = "Single public IP admitted through Key Vault's firewall (INV-2). Null keeps the public path closed."
  type        = string
  default     = null
}

variable "acr_firewall_allowed_cidrs" {
  description = "CIDR ranges admitted through ACR's firewall (INV-9). Empty keeps the public push path closed."
  type        = list(string)
  default     = []
}

variable "postgres_version" {
  type    = string
  default = "16"
}

variable "postgres_sku_name" {
  type    = string
  default = "GP_Standard_D2ds_v5"
}

variable "postgres_storage_mb" {
  type    = number
  default = 32768
}

variable "postgres_backup_retention_days" {
  type    = number
  default = 7
}

variable "postgres_admin_username" {
  type    = string
  default = "psqladmin"
}

variable "postgres_firewall_allowed_cidrs" {
  description = "CIDR ranges admitted through PostgreSQL's firewall (INV-1). Empty keeps the public path closed."
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Tags applied to every resource. Project = nats-saas is required (§5, Requirements)."
  type        = map(string)
  default = {
    Project = "nats-saas"
  }
}
