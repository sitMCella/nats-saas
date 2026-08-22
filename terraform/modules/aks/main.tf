resource "azurerm_public_ip" "aks" {
  name                = "pip-natssaas-${var.env}"
  resource_group_name = var.resource_group_name
  location            = var.location
  allocation_method   = "Static"
  sku                 = "Standard"
  tags                = var.tags
}

resource "azurerm_kubernetes_cluster" "this" {
  name                      = "aks-natssaas-${var.env}"
  resource_group_name       = var.resource_group_name
  location                  = var.location
  dns_prefix                = "aks-natssaas-${var.env}"
  kubernetes_version        = var.kubernetes_version
  oidc_issuer_enabled       = true
  workload_identity_enabled = true

  # azurerm 5.x requires this block explicitly; "Manual" keeps node pools
  # exactly as this project defines them (no Node Auto Provisioning/Karpenter).
  node_provisioning_profile {
    mode = "Manual"
  }

  default_node_pool {
    name                 = "system"
    vm_size              = var.system_node_vm_size
    vnet_subnet_id       = var.aks_subnet_id # cross-RG: snet-aks in the network RG
    auto_scaling_enabled = true
    min_count            = var.system_node_min_count
    max_count            = var.system_node_max_count
  }

  identity {
    type = "SystemAssigned"
  }

  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
    load_balancer_sku   = "standard"
    outbound_type       = "loadBalancer"
    load_balancer_profile {
      outbound_ip_address_ids = [azurerm_public_ip.aks.id]
    }
  }

  key_vault_secrets_provider {
    secret_rotation_enabled  = true
    secret_rotation_interval = "2m"
  }

  tags = var.tags
}
