variable "resource_group_name" {
  type = string
}

variable "location" {
  type = string
}

variable "env" {
  type = string
}

variable "kubernetes_version" {
  type = string
}

variable "aks_subnet_id" {
  description = "snet-aks, cross-RG reference into the network Resource Group."
  type        = string
}

variable "system_node_vm_size" {
  type = string
}

variable "system_node_min_count" {
  type = number
}

variable "system_node_max_count" {
  type = number
}

variable "tags" {
  type = map(string)
}
