# Look up a built-in archetype by name instead of hard-coding its OID.
data "midpoint_object" "application" {
  type = "archetypes"
  name = "Application"
}

resource "midpoint_service" "crm" {
  name      = "CRM"
  archetype = data.midpoint_object.application.oid
}

# Read an object by OID; body holds its canonical content.
data "midpoint_object" "end_user" {
  type = "roles"
  oid  = "00000000-0000-0000-0000-000000000008"
}

output "end_user_description" {
  value = try(data.midpoint_object.end_user.body.description, null)
}
