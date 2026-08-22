variable "resource_group_name" {
  type = string
}

variable "location" {
  type = string
}

variable "env" {
  type = string
}

variable "postgres_version" {
  type    = string
  default = "16"
}

variable "sku_name" {
  type = string
}

variable "storage_mb" {
  type = number
}

variable "backup_retention_days" {
  type = number
}

variable "admin_username" {
  type = string
}

variable "admin_password" {
  type      = string
  sensitive = true
}

variable "firewall_allowed_cidrs" {
  description = "CIDR ranges admitted through the firewall (INV-1). Empty admits none."
  type        = list(string)
  default     = []
}

variable "pe_subnet_id" {
  description = "snet-pe, cross-RG reference into the network Resource Group."
  type        = string
}

variable "postgres_private_dns_zone_id" {
  type = string
}

variable "tags" {
  type = map(string)
}
