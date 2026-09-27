variable "host_mounts" {
  description = "Opt-in host directories. Generate with sandbox-host.py mounts-plan/mounts-apply."
  type = map(object({
    source   = string
    path     = string
    readonly = optional(bool, true)
  }))
  default  = {}
  nullable = false

  validation {
    condition = alltrue([for name, mount in var.host_mounts :
      can(regex("^[a-z][a-z0-9-]{0,29}$", name)) &&
      startswith(mount.source, "/") && mount.source != "/" &&
      mount.source == abspath(mount.source) &&
      !can(regex("[,\\n\\r]", mount.source)) &&
      can(regex("^/workspace/[a-zA-Z0-9][a-zA-Z0-9._-]*$", mount.path))
    ])
    error_message = "Use named mounts with canonical absolute sources (no root, commas or newlines) and destinations directly under /workspace."
  }

  validation {
    condition     = length(distinct([for mount in var.host_mounts : mount.path])) == length(var.host_mounts)
    error_message = "Each mount must have a distinct /workspace destination."
  }
}

variable "mount_owner" {
  description = "Incus-host UID/GID mapped to agent (1001:1001), only when sharing directories. Never map host root."
  type        = object({ uid = number, gid = number })
  default     = null
  validation {
    condition = var.mount_owner == null ? true : alltrue([
      for id in [var.mount_owner.uid, var.mount_owner.gid] : id > 0 && id < 2147483647 && floor(id) == id
    ])
    error_message = "Mount ownership requires positive integer host UID/GID values."
  }
}

locals {
  mount_project_config = length(var.host_mounts) == 0 || var.mount_owner == null ? {} : {
    "restricted.devices.disk.paths" = join(",", sort(distinct([for mount in var.host_mounts : mount.source])))
    "restricted.idmap.uid"          = tostring(var.mount_owner.uid)
    "restricted.idmap.gid"          = tostring(var.mount_owner.gid)
  }
  mount_profile_config = length(var.host_mounts) == 0 || var.mount_owner == null ? {} : {
    "raw.idmap" = "uid ${var.mount_owner.uid} 1001\ngid ${var.mount_owner.gid} 1001"
  }
}
