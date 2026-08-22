# docs/terraform-infra/design.md §6 (Key outputs)

output "aks_cluster_name" {
  value = module.aks.cluster_name
}

output "aks_oidc_issuer_url" {
  description = "Consumed by the Helm/manifest layer to finish wiring Workload Identity for the API's ServiceAccount."
  value       = module.aks.oidc_issuer_url
}

output "aks_public_ip_address" {
  description = "The static IP the HUG Gateway's LoadBalancer Service and its DNS record should target."
  value       = module.aks.public_ip_address
}

output "key_vault_name" {
  value = module.key_vault.name
}

output "key_vault_uri" {
  value = module.key_vault.uri
}

output "acr_login_server" {
  value = module.container_registry.login_server
}

output "postgres_fqdn" {
  value = module.postgresql.fqdn
}

output "keycloak_identity_client_id" {
  value = azurerm_user_assigned_identity.keycloak.client_id
}

output "rest_api_identity_client_id" {
  value = azurerm_user_assigned_identity.rest_api.client_id
}
