output "workspace" {
  description = "Use this explicit endpoint/project for operator inspection; do not rely on the CLI default remote."
  value = {
    socket            = var.incus_socket
    project           = incus_project.sandbox.name
    instance          = incus_instance.workspace.name
    image_fingerprint = local.workspace_fingerprint
    architecture      = incus_instance.workspace.architecture
    network           = var.dev_network_enabled ? var.dev_network : "none"
  }
}
