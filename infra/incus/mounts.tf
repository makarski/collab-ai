variable "host_mounts" {
  description = "Project host-to-container mounts; container_readonly limits container writes and defaults to true. Generate with sandbox-host.py mounts-plan/mounts-apply."
  type = list(object({
    project_name         = string
    host_path            = string
    container_mount_path = string
    container_readonly   = optional(bool, true)
  }))
  default  = []
  nullable = false

  validation {
    condition = alltrue([for mount in var.host_mounts :
      can(regex("^[a-z][a-z0-9-]{0,29}$", mount.project_name)) &&
      startswith(mount.host_path, "/") && mount.host_path != "/" &&
      mount.host_path == abspath(mount.host_path) &&
      !can(regex("[,\\n\\r]", mount.host_path)) &&
      can(regex("^/workspace/[a-zA-Z0-9][a-zA-Z0-9._-]*$", mount.container_mount_path))
    ])
    error_message = "Use named mounts with canonical absolute sources (no root, commas or newlines) and destinations directly under /workspace."
  }

  validation {
    condition     = length(distinct([for mount in var.host_mounts : mount.project_name])) == length(var.host_mounts)
    error_message = "Each mount must have a distinct project_name."
  }

  validation {
    condition     = length(distinct([for mount in var.host_mounts : mount.container_mount_path])) == length(var.host_mounts)
    error_message = "Each mount must have a distinct container_mount_path directly under /workspace."
  }

  validation {
    condition = alltrue(flatten([for index, mount in var.host_mounts : [
      for other_index, other in var.host_mounts : index == other_index || (
        mount.host_path != other.host_path && !startswith(mount.host_path, "${other.host_path}/")
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
    "restricted.devices.disk.paths" = join(",", sort(distinct([for mount in var.host_mounts : mount.host_path])))
    }, var.share_identity == null ? {} : {
    "restricted.idmap.uid" = tostring(var.share_identity.uid)
    "restricted.idmap.gid" = tostring(var.share_identity.gid)
  })
  mount_profile_config = length(var.host_mounts) == 0 || var.share_identity == null ? {} : {
    "raw.idmap" = "uid ${var.share_identity.uid} 1001\ngid ${var.share_identity.gid} 1001"
  }
}
