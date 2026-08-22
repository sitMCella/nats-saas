resource "azurerm_postgresql_flexible_server" "this" {
  name                          = "psql-natssaas-${var.env}"
  resource_group_name           = var.resource_group_name
  location                      = var.location
  version                       = var.postgres_version
  sku_name                      = var.sku_name
  storage_mb                    = var.storage_mb
  backup_retention_days         = var.backup_retention_days
  administrator_login           = var.admin_username
  administrator_password        = var.admin_password
  public_network_access_enabled = true
  tags                          = var.tags

  lifecycle {
    prevent_destroy = true # INV-4
  }
}

resource "azurerm_postgresql_flexible_server_firewall_rule" "allowed" {
  for_each = toset(var.firewall_allowed_cidrs) # INV-1: empty by default

  name             = "allow-${replace(each.value, "/", "-")}"
  server_id        = azurerm_postgresql_flexible_server.this.id
  start_ip_address = cidrhost(each.value, 0)
  end_ip_address   = cidrhost(each.value, -1)
}

resource "azurerm_private_endpoint" "postgres" {
  name                = "pe-psql-natssaas-${var.env}"
  resource_group_name = var.resource_group_name
  location            = var.location
  subnet_id           = var.pe_subnet_id # cross-RG: snet-pe in the network RG, shared with Key Vault's PE
  tags                = var.tags

  private_service_connection {
    name                           = "psc-psql-natssaas-${var.env}"
    private_connection_resource_id = azurerm_postgresql_flexible_server.this.id
    subresource_names              = ["postgresqlServer"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [var.postgres_private_dns_zone_id]
  }
}
