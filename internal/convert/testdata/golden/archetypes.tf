resource "midpoint_archetype" "application_role" {
  name           = "Application role"
  oid            = "00000000-0000-0000-0000-000000000328"
  description    = "Application roles are basic roles related to applications."
  holder_type    = ["RoleType"]
  adopt_existing = true
}

resource "midpoint_archetype" "platform_item" {
  name        = "Platform item"
  oid         = "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02"
  description = "Marks services and business roles of the platform catalogue."

  archetype_policy = {
    display = {
      label       = "Platform item"
      pluralLabel = "Platform items"
    }
  }
}

resource "midpoint_archetype" "tenant" {
  name        = "Tenant"
  oid         = "d2f84b19-7a03-4e5c-8b66-1f0c9e3a7d66"
  description = "Organizations that are tenants."

  archetype_policy = {
    display = { label = "Tenant", pluralLabel = "Tenants" }
  }
}
