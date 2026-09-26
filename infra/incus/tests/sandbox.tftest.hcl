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
