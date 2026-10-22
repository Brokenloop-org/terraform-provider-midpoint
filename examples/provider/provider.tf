terraform {
  required_providers {
    midpoint = {
      source = "jplana/midpoint"
    }
  }
}

variable "midpoint_username" {
  type = string
}

variable "midpoint_password" {
  type      = string
  sensitive = true
}

provider "midpoint" {
  url      = "https://midpoint.example.com" # or MIDPOINT_URL
  username = var.midpoint_username          # or MIDPOINT_USERNAME
  password = var.midpoint_password          # or MIDPOINT_PASSWORD

  # Optional: a private CA instead of skipping verification (insecure = true).
  # ca_bundle = file("${path.module}/ca.pem")

  # Optional: the execution options of every write of a resource type,
  # here none for roles (instead of the built-in isImport and raw).
  # default_options = {
  #   role = []
  # }
}
