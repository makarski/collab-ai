terraform {
  required_version = ">= 1.9.0, < 2.0.0"

  required_providers {
    incus = {
      source  = "registry.terraform.io/lxc/incus"
      version = "1.2.0"
    }
  }
}
