provider "incus" {
  default_remote = "sandbox"

  remote {
    name    = "sandbox"
    address = "unix://${var.incus_socket}"
  }

  remote {
    name     = "sandbox-images"
    address  = "https://images.linuxcontainers.org"
    protocol = "simplestreams"
    public   = true
  }
}

resource "incus_project" "sandbox" {
  depends_on = [terraform_data.secured_preflight]

  name          = var.project_name
  description   = "collab-ai offline sandbox, managed by infra/incus"
  force_destroy = false

  config = merge({
    "features.images"                 = "true"
    "features.profiles"               = "true"
    "features.storage.volumes"        = "true"
    "restricted"                      = "true"
    "restricted.containers.privilege" = "isolated"
    "restricted.containers.nesting"   = "block"
    "restricted.devices.disk"         = length(var.host_mounts) > 0 ? "allow" : (var.secured_runtime ? "managed" : "block")
    "restricted.devices.nic"          = "block"
    "restricted.devices.proxy"        = "block"
    "limits.containers"               = var.secured_runtime ? "2" : "1"
    "limits.virtual-machines"         = "0"
  }, local.mount_project_config)

  lifecycle {
    precondition {
      condition     = length(var.host_mounts) == 0 || var.mount_owner != null
      error_message = "Host mounts require mount_owner; use sandbox-host.py mounts-apply to generate it."
    }
  }
}

locals {
  workspace_fingerprint = one(concat(incus_image.base[*].fingerprint, incus_image.workspace[*].fingerprint))
}

moved {
  from = incus_image.base
  to   = incus_image.base[0]
}

resource "incus_image" "base" {
  count   = var.image_file == null ? 1 : 0
  project = incus_project.sandbox.name
  source_image = {
    remote       = "sandbox-images"
    name         = var.image_fingerprint
    type         = "container"
    copy_aliases = false
  }
}

resource "incus_image" "workspace" {
  count   = var.image_file == null ? 0 : 1
  project = incus_project.sandbox.name
  source_file = {
    data_path = var.image_file
  }
  lifecycle {
    precondition {
      condition     = var.image_file == null ? true : try(filesha256(var.image_file) == var.image_fingerprint, false)
      error_message = "The workspace image file must exist and match image_fingerprint. Rebuild or restore the original artifact."
    }
    postcondition {
      condition     = self.fingerprint == var.image_fingerprint
      error_message = "Imported image fingerprint differs from the planned artifact; refusing to create the workspace."
    }
  }
}

resource "incus_profile" "sandbox" {
  name    = "offline"
  project = incus_project.sandbox.name

  config = merge({
    "security.privileged"     = "false"
    "security.nesting"        = "false"
    "security.idmap.isolated" = "true"
    "security.guestapi"       = "false"
    "boot.autostart"          = "false"
    "limits.cpu"              = tostring(var.cpu_count)
    "limits.memory"           = "${var.memory_gib}GiB"
    "limits.processes"        = "512"
  }, local.mount_profile_config)

  # Deliberately no NIC, proxy or inherited default profile. Host mounts opt in.
  device {
    name = "root"
    type = "disk"
    properties = {
      path = "/"
      pool = var.storage_pool
      size = "${var.disk_gib}GiB"
    }
  }

  dynamic "device" {
    for_each = incus_storage_volume.budget_status
    content {
      name = "budget-status"
      type = "disk"
      properties = {
        source   = device.value.name
        pool     = var.storage_pool
        path     = "/mnt/collab-status"
        readonly = "true"
      }
    }
  }

  dynamic "device" {
    for_each = var.host_mounts
    content {
      name = "host-${device.key}"
      type = "disk"
      properties = {
        source   = device.value.source
        path     = device.value.path
        readonly = tostring(device.value.readonly)
        required = "true"
      }
    }
  }
}

resource "incus_instance" "workspace" {
  name      = "workspace"
  project   = incus_project.sandbox.name
  image     = local.workspace_fingerprint
  type      = "container"
  profiles  = [incus_profile.sandbox.name]
  ephemeral = false
  running   = var.running

  config = {
    "user.collab-ai.managed" = "infra/incus"
  }
}
