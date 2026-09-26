variable "incus_socket" {
  description = "Absolute path to the operator's Incus Unix socket. Never mounted into the container."
  type        = string
  nullable    = false
  validation {
    condition     = startswith(var.incus_socket, "/") && !strcontains(var.incus_socket, "://")
    error_message = "Use an absolute local socket path, not a remote URL or ~ expansion."
  }
}

variable "image_fingerprint" {
  description = "Full SHA256 of a container image on images.linuxcontainers.org; aliases are rejected."
  type        = string
  nullable    = false
  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.image_fingerprint))
    error_message = "Pin the full 64-character lowercase image fingerprint; do not use a moving alias."
  }
}

variable "project_name" {
  type        = string
  default     = "collab-ai"
  description = "Dedicated project owned by this state. Existing projects must not be adopted automatically."
  validation {
    condition     = can(regex("^collab-[a-z0-9-]+$", var.project_name)) && length(var.project_name) <= 40
    error_message = "Use a dedicated name beginning collab-, with lowercase letters, digits and hyphens (40 characters maximum)."
  }
}

variable "storage_pool" {
  type        = string
  default     = "default"
  description = "Existing Incus storage pool with root-volume quota support, such as Colima's default ZFS pool."
}

variable "cpu_count" {
  type    = number
  default = 2
  validation {
    condition     = var.cpu_count >= 1 && var.cpu_count <= 8 && floor(var.cpu_count) == var.cpu_count
    error_message = "cpu_count must be an integer from 1 through 8."
  }
}

variable "memory_gib" {
  type    = number
  default = 4
  validation {
    condition     = var.memory_gib >= 1 && var.memory_gib <= 16 && floor(var.memory_gib) == var.memory_gib
    error_message = "memory_gib must be an integer from 1 through 16."
  }
}

variable "disk_gib" {
  type    = number
  default = 10
  validation {
    condition     = var.disk_gib >= 2 && var.disk_gib <= 100 && floor(var.disk_gib) == var.disk_gib
    error_message = "disk_gib must be an integer from 2 through 100."
  }
}

variable "running" {
  description = "Desired power state; false stops the instance while retaining its root disk."
  type        = bool
  default     = true
}
