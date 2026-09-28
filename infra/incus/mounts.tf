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

  validation {
    condition = alltrue(flatten([for name, mount in var.host_mounts : [
      for other_name, other in var.host_mounts : name == other_name || (
        mount.source != other.source && !startswith(mount.source, "${other.source}/")
      )
    ]]))
    error_message = "Mount source directories must not repeat or overlap."
  }
}

variable "share_identity" {
  description = "Dedicated Linux sharing account IDs, resolved by sandbox-host.py --share-user. Never use operator/admin IDs."
  type        = object({ uid = number, gid = number })
  default     = null
  validation {
    condition = var.share_identity == null ? true : alltrue([
      for id in [var.share_identity.uid, var.share_identity.gid] : id > 0 && id < 2147483647 && floor(id) == id
    ])
    error_message = "The dedicated sharing account requires positive integer host UID/GID values."
  }
}

locals {
  mount_project_config = length(var.host_mounts) == 0 ? {} : merge({
    "restricted.devices.disk.paths" = join(",", sort(distinct([for mount in var.host_mounts : mount.source])))
    }, var.share_identity == null ? {} : {
    "restricted.idmap.uid" = tostring(var.share_identity.uid)
    "restricted.idmap.gid" = tostring(var.share_identity.gid)
  })
  mount_profile_config = length(var.host_mounts) == 0 || var.share_identity == null ? {} : {
    "raw.idmap" = "uid ${var.share_identity.uid} 1001\ngid ${var.share_identity.gid} 1001"
  }
}
