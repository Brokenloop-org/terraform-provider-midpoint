resource "midpoint_object_template" "acme_person_template" {
  name        = "Acme Person Template"
  oid         = "a395aa95-3aea-4db3-893c-516cf8ca422b"
  description = "Generates the full name of a person."

  mapping = [
    {
      expression = {
        script = [
          { code = "basic.concatName(givenName, familyName)" },
        ]
      }
      name = "fullName"
      source = [
        { path = "givenName" },
        { path = "familyName" },
      ]
      strength = "weak"
      target   = { path = "fullName" }
    },
  ]
}
