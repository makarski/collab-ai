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
  name          = var.project_name
  description   = "collab-ai offline sandbox, managed by infra/incus"
  force_destroy = false

  config = {
    "features.images"                 = "true"
    "features.profiles"               = "true"
    "features.storage.volumes"        = "true"
    "restricted"                      = "true"
    "restricted.containers.privilege" = "isolated"
    "restricted.containers.nesting"   = "block"
    "restricted.devices.disk"         = "block"
    "restricted.devices.nic"          = "block"
    "restricted.devices.proxy"        = "block"
    "limits.containers"               = "1"
    "limits.virtual-machines"         = "0"
  }
}

resource "incus_image" "base" {
  project = incus_project.sandbox.name
  source_image = {
    remote       = "sandbox-images"
    name         = var.image_fingerprint
    type         = "container"
    copy_aliases = false
  }
}

resource "incus_profile" "sandbox" {
  name    = "offline"
  project = incus_project.sandbox.name

  config = {
    "security.privileged"     = "false"
    "security.nesting"        = "false"
    "security.idmap.isolated" = "true"
    "security.guestapi"       = "false"
    "boot.autostart"          = "false"
    "limits.cpu"              = tostring(var.cpu_count)
    "limits.memory"           = "${var.memory_gib}GiB"
    "limits.processes"        = "512"
  }

  # Deliberately no NIC, host bind mount, proxy or inherited default profile.
  device {
    name = "root"
    type = "disk"
    properties = {
      path = "/"
      pool = var.storage_pool
      size = "${var.disk_gib}GiB"
    }
  }
}

resource "incus_instance" "workspace" {
  name      = "workspace"
  project   = incus_project.sandbox.name
  image     = incus_image.base.fingerprint
  type      = "container"
  profiles  = [incus_profile.sandbox.name]
  ephemeral = false
  running   = var.running

  config = {
    "user.collab-ai.managed" = "infra/incus"
  }
}
