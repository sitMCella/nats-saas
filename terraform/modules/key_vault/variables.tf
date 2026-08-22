variable "resource_group_name" {
  type = string
}

variable "location" {
  type = string
}

variable "env" {
  type = string
}

variable "tenant_id" {
  type = string
}

variable "allowed_public_ip" {
  description = "Single public IP admitted through the firewall (INV-2). Null admits none."
  type        = string
  default     = null
}

variable "pe_subnet_id" {
  description = "snet-pe, cross-RG reference into the network Resource Group."
  type        = string
}

variable "key_vault_private_dns_zone_id" {
  type = string
}

variable "tags" {
  type = map(string)
}
