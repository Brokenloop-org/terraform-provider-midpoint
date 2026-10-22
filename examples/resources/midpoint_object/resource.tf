# A function library, given as the midPoint object without its type wrapper.
resource "midpoint_object" "greetings" {
  type = "functionLibraries"
  oid  = "7d4e2f8a-3c1b-4e6a-9f0d-2b5c8a1e4f36"

  body = {
    name        = "greetings"
    description = "Helper functions for mappings"
    function = [{
      name       = "greet"
      returnType = "xsd:string"
      parameter  = [{ name = "who", type = "xsd:string" }]
      script     = [{ code = "'hello ' + who" }]
    }]
  }
}

# The body can come from a file exported from midPoint.
resource "midpoint_object" "report" {
  type = "reports"
  body = yamldecode(file("${path.module}/report.yaml"))
}

# Customize a built-in object: adopt it instead of failing because it exists.
# The body is authoritative, so it must hold the whole object.
resource "midpoint_object" "end_user" {
  type           = "roles"
  oid            = "00000000-0000-0000-0000-000000000008"
  adopt_existing = true
  body           = yamldecode(file("${path.module}/end-user-role.yaml"))
}
