variable "resource_group_name" {
  type = string
}

variable "location" {
  type = string
}

variable "env" {
  type = string
}

variable "vnet_address_space" {
  type = string
}

variable "aks_subnet_cidr" {
  type = string
}

variable "pe_subnet_cidr" {
  type = string
}

variable "tags" {
  type = map(string)
}
