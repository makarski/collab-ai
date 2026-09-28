mock_provider "incus" {}

variables {
  incus_socket      = "/tmp/test-incus.sock"
  image_fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

run "offline_workspace" {
  command = plan

  assert {
    condition = (
      incus_project.sandbox.config["restricted"] == "true" &&
      incus_project.sandbox.config["restricted.devices.nic"] == "block" &&
      incus_project.sandbox.config["restricted.devices.proxy"] == "block" &&
      incus_project.sandbox.config["restricted.devices.disk"] == "block" &&
      incus_project.sandbox.config["restricted.containers.privilege"] == "isolated"
    )
    error_message = "The project must reject networking, host bind mounts and privileged containers."
  }

  assert {
    condition = (
      length(incus_profile.sandbox.device) == 1 &&
      one(incus_profile.sandbox.device).type == "disk" &&
      one(incus_profile.sandbox.device).properties.path == "/" &&
      one(incus_profile.sandbox.device).properties.pool == "default" &&
      one(incus_profile.sandbox.device).properties.size == "10GiB" &&
      !contains(keys(one(incus_profile.sandbox.device).properties), "source")
    )
    error_message = "The only device must be a quota-bound managed root disk, with no host path or NIC."
  }

  assert {
    condition = (
      incus_profile.sandbox.config["security.privileged"] == "false" &&
      incus_profile.sandbox.config["security.idmap.isolated"] == "true" &&
      incus_profile.sandbox.config["security.nesting"] == "false" &&
      incus_profile.sandbox.config["security.guestapi"] == "false" &&
      incus_profile.sandbox.config["boot.autostart"] == "false" &&
      incus_profile.sandbox.config["limits.processes"] == "512" &&
      length(incus_instance.workspace.profiles) == 1 &&
      one(incus_instance.workspace.profiles) == incus_profile.sandbox.name &&
      incus_instance.workspace.ephemeral == false &&
      incus_project.sandbox.force_destroy == false
    )
    error_message = "Isolation, bounded processes and explicit profile ownership must survive configuration changes."
  }
}

run "configured_limits_and_stop" {
  command = plan
  variables {
    cpu_count  = 3
    memory_gib = 6
    disk_gib   = 12
    running    = false
  }
  assert {
    condition = (
      incus_profile.sandbox.config["limits.cpu"] == "3" &&
      incus_profile.sandbox.config["limits.memory"] == "6GiB" &&
      one(incus_profile.sandbox.device).properties.size == "12GiB" &&
      incus_instance.workspace.running == false
    )
    error_message = "Changing resources or desired power state must affect the planned workspace."
  }
}

run "reject_moving_image" {
  command = plan
  variables {
    image_fingerprint = "ubuntu/24.04"
  }
  expect_failures = [var.image_fingerprint]
}

run "local_workspace_image" {
  command = plan
  override_resource {
    target = incus_image.workspace
    values = {
      # SHA256 of tests/fixtures/image.txt; OpenTofu evaluates this at plan time.
      fingerprint = "023d926408ae9494f18ccfae5057a3f9a34e6b107f10527373fb7138505af9a8"
    }
  }
  variables {
    image_file        = abspath("tests/fixtures/image.txt")
    image_fingerprint = filesha256("tests/fixtures/image.txt")
  }
  assert {
    condition = (
      length(incus_image.base) == 0 &&
      incus_image.workspace[0].source_file.data_path == var.image_file
    )
    error_message = "A built workspace must be imported from the verified local artifact, never an upstream base."
  }
}

run "reject_changed_image_artifact" {
  command = plan
  variables {
    image_file = abspath("tests/fixtures/image.txt")
  }
  expect_failures = [incus_image.workspace[0]]
}

run "reject_remote_endpoint" {
  command = plan
  variables {
    incus_socket = "https://example.com:8443"
  }
  expect_failures = [var.incus_socket]
}

run "reject_default_project" {
  command = plan
  variables {
    project_name = "default"
  }
  expect_failures = [var.project_name]
}

run "reject_unbounded_resources" {
  command = plan
  variables {
    cpu_count  = 0
    memory_gib = 0
    disk_gib   = 0
  }
  expect_failures = [var.cpu_count, var.memory_gib, var.disk_gib]
}

run "selected_host_mounts" {
  command = plan
  variables {
    host_mounts = {
      source = { source = "/home/operator/source", path = "/workspace/source" }
      work   = { source = "/home/operator/work", path = "/workspace/work", readonly = false }
    }
    mount_owner = { uid = 501, gid = 20 }
  }
  assert {
    condition = (
      incus_project.sandbox.config["restricted.devices.disk.paths"] == "/home/operator/source,/home/operator/work" &&
      incus_project.sandbox.config["restricted.idmap.uid"] == "501" &&
      incus_project.sandbox.config["restricted.devices.nic"] == "block" &&
      incus_profile.sandbox.config["raw.idmap"] == "uid 501 1001\ngid 20 1001" &&
      incus_profile.sandbox.config["security.idmap.isolated"] == "true"
    )
    error_message = "Only selected paths and one non-root host identity may be shared."
  }
  assert {
    condition = alltrue([for device in incus_profile.sandbox.device :
      device.name == "root" ? true : device.properties.readonly == (device.name == "host-source" ? "true" : "false")
    ])
    error_message = "Mounts must be read-only unless explicitly writable."
  }
}

run "reject_root_mount" {
  command = plan
  variables {
    host_mounts = { bad = { source = "/", path = "/workspace/root" } }
    mount_owner = { uid = 501, gid = 20 }
  }
  expect_failures = [var.host_mounts]
}

run "reject_escaping_target" {
  command = plan
  variables {
    host_mounts = { bad = { source = "/home/operator/repo", path = "/workspace/../etc" } }
    mount_owner = { uid = 501, gid = 20 }
  }
  expect_failures = [var.host_mounts]
}

run "reject_missing_mount_owner" {
  command = plan
  variables {
    host_mounts = { repo = { source = "/home/operator/repo", path = "/workspace/repo" } }
  }
  expect_failures = [incus_project.sandbox]
}

run "reject_root_identity" {
  command = plan
  variables {
    mount_owner = { uid = 0, gid = 20 }
  }
  expect_failures = [var.mount_owner]
}

run "reject_nested_mount_sources" {
  command = plan
  variables {
    host_mounts = {
      repo = { source = "/home/operator/repo", path = "/workspace/repo" }
      nested = { source = "/home/operator/repo/subdir", path = "/workspace/nested" }
    }
    mount_owner = { uid = 501, gid = 20 }
  }
  expect_failures = [var.host_mounts]
}

run "secured_runtime" {
  command = plan
  variables {
    secured_runtime = true
    running = false
    host_mounts = { repo = { source = "/home/operator/repo", path = "/workspace/repo", readonly = false } }
    mount_owner = { uid = 501, gid = 20 }
  }
  assert {
    condition = (
      incus_project.sandbox.config["limits.containers"] == "2" &&
      incus_instance.secured[0].running == false &&
      incus_instance.workspace.running == false &&
      length(incus_instance.secured[0].profiles) == 1 &&
      one(incus_instance.secured[0].profiles) == incus_profile.secured[0].name &&
      incus_profile.secured[0].config["security.idmap.isolated"] == "true" &&
      !contains(keys(incus_profile.secured[0].config), "raw.idmap")
    )
    error_message = "Secured must have its own isolated identity/profile and follow the desired power state."
  }
  assert {
    condition = (
      length(incus_profile.secured[0].device) == 5 &&
      alltrue([for device in incus_profile.secured[0].device : device.type == "disk" && contains(keys(device.properties), "pool")]) &&
      alltrue([for device in incus_profile.sandbox.device : device.name != "secured-state"]) &&
      one([for device in incus_profile.sandbox.device : device if device.name == "budget-status"]).properties.readonly == "true" &&
      one([for device in incus_profile.secured[0].device : device if device.name == "dev-executor"]).properties.readonly == "true" &&
      one([for device in incus_profile.sandbox.device : device if device.name == "broker-ipc"]).properties.readonly == "true" &&
      one([for device in incus_profile.secured[0].device : device if device.name == "broker-ipc"]).properties.source == incus_storage_volume.broker_ipc[0].name
    )
    error_message = "Secured must have only managed volumes; dev must receive only read-only status, never private state."
  }
  assert {
    condition = (
      incus_storage_volume.secured_state[0].config["security.shifted"] == "true" &&
      incus_storage_volume.budget_status[0].config["security.shifted"] == "true" &&
      incus_storage_volume.dev_executor[0].config["security.shifted"] == "true" &&
      incus_storage_volume.broker_ipc[0].config["security.shifted"] == "true"
    )
    error_message = "Persistent volumes must support isolated mappings; boot permissions are checked by the live smoke test."
  }
}

run "secured_without_host_mounts" {
  command = plan
  variables {
    secured_runtime = true
  }
  assert {
    condition = (
      incus_project.sandbox.config["restricted.devices.disk"] == "managed" &&
      !contains(keys(incus_project.sandbox.config), "restricted.devices.disk.paths")
    )
    error_message = "Secured IPC must allow managed volumes while rejecting host bind mounts."
  }
}
