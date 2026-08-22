resource "azurerm_key_vault" "this" {
  name                          = "kv-natssaas-${var.env}"
  resource_group_name           = var.resource_group_name
  location                      = var.location
  tenant_id                     = var.tenant_id
  sku_name                      = "standard"
  rbac_authorization_enabled    = true # renamed from enable_rbac_authorization in azurerm 5.x
  purge_protection_enabled      = true
  public_network_access_enabled = true
  tags                          = var.tags

  network_acls {
    default_action = "Deny"
    bypass         = "AzureServices"
    ip_rules       = var.allowed_public_ip == null ? [] : [var.allowed_public_ip] # INV-2
  }

  lifecycle {
    prevent_destroy = true # INV-4
  }
}

resource "azurerm_private_endpoint" "key_vault" {
  name                = "pe-kv-natssaas-${var.env}"
  resource_group_name = var.resource_group_name
  location            = var.location
  subnet_id           = var.pe_subnet_id # cross-RG: snet-pe in the network RG
  tags                = var.tags

  private_service_connection {
    name                           = "psc-kv-natssaas-${var.env}"
    private_connection_resource_id = azurerm_key_vault.this.id
    subresource_names              = ["vault"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [var.key_vault_private_dns_zone_id]
  }
}
