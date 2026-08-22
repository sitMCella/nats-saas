module "rg_network" {
  source   = "./modules/resource_group"
  name     = var.network_resource_group_name
  location = var.location
  tags     = var.tags
}

module "rg_platform" {
  source   = "./modules/resource_group"
  name     = var.platform_resource_group_name
  location = var.location
  tags     = var.tags
}

module "rg_data" {
  source   = "./modules/resource_group"
  name     = var.data_resource_group_name
  location = var.location
  tags     = var.tags
}

module "network" {
  source              = "./modules/network"
  resource_group_name = module.rg_network.name
  location            = var.location
  env                 = var.env
  vnet_address_space  = var.vnet_address_space
  aks_subnet_cidr     = var.aks_subnet_cidr
  pe_subnet_cidr      = var.pe_subnet_cidr
  tags                = var.tags
}

module "key_vault" {
  source                        = "./modules/key_vault"
  resource_group_name           = module.rg_platform.name
  location                      = var.location
  env                           = var.env
  tenant_id                     = var.tenant_id
  allowed_public_ip             = var.key_vault_allowed_public_ip
  pe_subnet_id                  = module.network.pe_subnet_id
  key_vault_private_dns_zone_id = module.network.key_vault_private_dns_zone_id
  tags                          = var.tags
}

module "aks" {
  source                = "./modules/aks"
  resource_group_name   = module.rg_platform.name
  location              = var.location
  env                   = var.env
  kubernetes_version    = var.aks_kubernetes_version
  aks_subnet_id         = module.network.aks_subnet_id
  system_node_vm_size   = var.aks_system_node_vm_size
  system_node_min_count = var.aks_system_node_min_count
  system_node_max_count = var.aks_system_node_max_count
  tags                  = var.tags
}

resource "azurerm_role_assignment" "aks_network_contributor" {
  scope                = module.network.vnet_id
  role_definition_name = "Network Contributor"
  principal_id         = module.aks.kubelet_identity_object_id

  depends_on = [module.network, module.aks]
}

module "container_registry" {
  source                  = "./modules/container_registry"
  resource_group_name     = module.rg_platform.name
  location                = var.location
  env                     = var.env
  firewall_allowed_cidrs  = var.acr_firewall_allowed_cidrs
  pe_subnet_id            = module.network.pe_subnet_id
  acr_private_dns_zone_id = module.network.acr_private_dns_zone_id
  tags                    = var.tags
}

resource "azurerm_role_assignment" "aks_acr_pull" {
  scope                = module.container_registry.id
  role_definition_name = "AcrPull"
  principal_id         = module.aks.kubelet_identity_object_id

  depends_on = [module.aks, module.container_registry]
}

resource "random_password" "postgres_admin" {
  length  = 32
  special = true
}

module "postgresql" {
  source                       = "./modules/postgresql"
  resource_group_name          = module.rg_data.name
  location                     = var.location
  env                          = var.env
  postgres_version             = var.postgres_version
  sku_name                     = var.postgres_sku_name
  storage_mb                   = var.postgres_storage_mb
  backup_retention_days        = var.postgres_backup_retention_days
  admin_username               = var.postgres_admin_username
  admin_password               = random_password.postgres_admin.result
  firewall_allowed_cidrs       = var.postgres_firewall_allowed_cidrs
  pe_subnet_id                 = module.network.pe_subnet_id
  postgres_private_dns_zone_id = module.network.postgres_private_dns_zone_id
  tags                         = var.tags
}

resource "azurerm_key_vault_secret" "postgres_admin_password" {
  name         = "postgres-admin-password"
  value        = random_password.postgres_admin.result
  key_vault_id = module.key_vault.id
}
