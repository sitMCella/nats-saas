variable "resource_group_name" {
  type = string
}

variable "location" {
  type = string
}

variable "env" {
  type = string
}

variable "firewall_allowed_cidrs" {
  description = "CIDR ranges admitted through the firewall (INV-9). Empty admits none."
  type        = list(string)
  default     = []
}

variable "pe_subnet_id" {
  description = "snet-pe, cross-RG reference into the network Resource Group."
  type        = string
}

variable "acr_private_dns_zone_id" {
  type = string
}

variable "tags" {
  type = map(string)
}
