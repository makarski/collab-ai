mock_provider "incus" {}

variables {
  incus_socket      = "/tmp/test-incus.sock"
  image_fingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  secured_runtime   = true
}

run "dev_online_by_default_control_offline" {
  command = plan
  assert {
    condition = (
      incus_project.sandbox.config["features.networks"] == "false" &&
      incus_project.sandbox.config["restricted.devices.nic"] == "managed" &&
      incus_project.sandbox.config["restricted.networks.access"] == "incusbr0" &&
      one([for device in incus_profile.sandbox.device : device if device.type == "nic"]).properties.network == "incusbr0" &&
      alltrue([for device in incus_profile.secured[0].device : device.type == "disk"]) &&
      output.workspace.network == "incusbr0" && output.secured.network == "none"
    )
    error_message = "Only dev must get a NIC by default, limited to the configured managed network."
  }
}

run "offline_opt_out" {
  command = plan
  variables {
    dev_network_enabled = false
  }
  assert {
    condition = (
      alltrue([for device in incus_profile.sandbox.device : device.type == "disk"]) &&
      alltrue([for device in incus_profile.secured[0].device : device.type == "disk"]) &&
      output.workspace.network == "none" && output.secured.network == "none"
    )
    error_message = "The offline opt-out must remove dev networking while keeping control offline."
  }
}

run "custom_managed_bridge" {
  command = plan
  variables {
    dev_network = "devbr0"
  }
  assert {
    condition = (
      incus_project.sandbox.config["restricted.networks.access"] == "devbr0" &&
      one([for device in incus_profile.sandbox.device : device if device.type == "nic"]).properties.network == "devbr0"
    )
    error_message = "Both the project allowlist and dev NIC must use the selected bridge."
  }
}

run "reject_multiple_networks" {
  command = plan
  variables {
    dev_network = "incusbr0,other"
  }
  expect_failures = [var.dev_network]
}
