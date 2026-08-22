output "vnet_id" {
  value = azurerm_virtual_network.this.id
}

output "aks_subnet_id" {
  value = azurerm_subnet.aks.id
}

output "pe_subnet_id" {
  value = azurerm_subnet.pe.id
}

output "key_vault_private_dns_zone_id" {
  value = azurerm_private_dns_zone.key_vault.id
}

output "postgres_private_dns_zone_id" {
  value = azurerm_private_dns_zone.postgres.id
}

output "acr_private_dns_zone_id" {
  value = azurerm_private_dns_zone.acr.id
}
