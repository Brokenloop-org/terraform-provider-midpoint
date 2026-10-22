# A user template that computes fullName from givenName and familyName.
resource "midpoint_object_template" "user" {
  name            = "User template"
  description     = "Computes the full name of every user."
  lifecycle_state = "active"

  # A list of mappings; one mapping object is accepted too.
  mapping = [
    yamldecode(<<-EOT
      name: full-name
      strength: weak
      source:
        - path: givenName
        - path: familyName
      expression:
        script:
          code: "basic.concatName(givenName, familyName)"
      target:
        path: fullName
    EOT
    ),
  ]

  item = [{
    ref         = "description"
    displayName = "Description"
  }]

  iteration_specification = { maxIterations = 5 }
}
