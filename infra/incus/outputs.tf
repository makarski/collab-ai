output "workspace" {
  description = "Use this explicit endpoint/project for operator inspection; do not rely on the CLI default remote."
  value = {
    socket            = var.incus_socket
    project           = incus_project.sandbox.name
    instance          = incus_instance.workspace.name
    image_fingerprint = incus_image.base.fingerprint
    architecture      = incus_instance.workspace.architecture
    network           = "none"
  }
}
