resource "azurerm_container_registry" "this" {
  name                          = "acrnatssaas${var.env}" # no hyphens allowed, see design doc §6 Naming and identity
  resource_group_name           = var.resource_group_name
  location                      = var.location
  sku                           = "Premium" # required for private_endpoint support
  admin_enabled                 = false     # INV-11: no static registry credential
  public_network_access_enabled = true
  tags                          = var.tags

  # azurerm 5.x moved network_rule_set from a nested block to a plain object
  # attribute; assign it directly instead of a `dynamic` block.
  network_rule_set = [{
    default_action = "Deny"
    ip_rule = [
      for cidr in var.firewall_allowed_cidrs : { # INV-9: empty by default
        action   = "Allow"
        ip_range = cidr
      }
    ]
  }]

  lifecycle {
    prevent_destroy = true # INV-4
  }
}

resource "azurerm_private_endpoint" "acr" {
  name                = "pe-acr-natssaas-${var.env}"
  resource_group_name = var.resource_group_name
  location            = var.location
  subnet_id           = var.pe_subnet_id # cross-RG: snet-pe in the network RG, shared with Key Vault's and PostgreSQL's PEs
  tags                = var.tags

  private_service_connection {
    name                           = "psc-acr-natssaas-${var.env}"
    private_connection_resource_id = azurerm_container_registry.this.id
    subresource_names              = ["registry"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [var.acr_private_dns_zone_id]
  }
}
