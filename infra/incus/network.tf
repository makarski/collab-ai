variable "dev_network_enabled" {
  description = "Give dev network access through a managed bridge. Set false for an offline workspace; control always has no NIC."
  type        = bool
  default     = true
  nullable    = false
}

variable "dev_network" {
  description = "Existing managed Incus bridge in the default project, with DNS/DHCP and outbound routing/NAT."
  type        = string
  default     = "incusbr0"
  nullable    = false
  validation {
    condition     = can(regex("^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$", var.dev_network))
    error_message = "dev_network must name one existing managed Incus bridge."
  }
}
