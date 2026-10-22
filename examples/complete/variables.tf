variable "midpoint_url" {
  description = "Base URL of the midPoint server, e.g. https://midpoint.example.com."
  type        = string
}

variable "midpoint_username" {
  description = "midPoint user for HTTP basic authentication."
  type        = string
  default     = "administrator"
}

variable "midpoint_password" {
  description = "Password of midpoint_username."
  type        = string
  sensitive   = true
}

variable "service_roles" {
  description = "Roles that grant access to the service, keyed by identifier."
  type = map(object({
    display_name = string
    risk_level   = string
  }))
  default = {
    admin  = { display_name = "Dev cluster / admin", risk_level = "high" }
    viewer = { display_name = "Dev cluster / viewer", risk_level = "low" }
  }
}
