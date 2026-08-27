output "cluster_id" {
  value = azurerm_kubernetes_cluster.this.id
}

output "cluster_name" {
  value = azurerm_kubernetes_cluster.this.name
}

output "oidc_issuer_url" {
  value = azurerm_kubernetes_cluster.this.oidc_issuer_url
}

output "cluster_identity_principal_id" {
  description = "System-assigned managed identity principal ID"
  value = azurerm_kubernetes_cluster.this.identity[0].principal_id
}

output "kubelet_identity_object_id" {
  description = "Kubelet managed identity object ID"
  value = azurerm_kubernetes_cluster.this.kubelet_identity[0].object_id
}

output "public_ip_address" {
  value = azurerm_public_ip.aks.ip_address
}
