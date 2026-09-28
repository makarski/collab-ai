resource "incus_storage_volume" "dev_executor" {
  count        = var.secured_runtime ? 1 : 0
  name         = "dev-executor"
  project      = incus_project.sandbox.name
  pool         = var.storage_pool
  type         = "custom"
  content_type = "filesystem"
  config = {
    "size"             = "16MiB"
    "security.shifted" = "true"
  }
}
