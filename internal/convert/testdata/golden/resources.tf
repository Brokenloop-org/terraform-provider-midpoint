resource "midpoint_resource" "users_csv" {
  name            = "Users CSV"
  oid             = "25e166d6-9899-43b7-b0d7-4c4c526b7a9e"
  lifecycle_state = "active"

  connector = {
    type    = "com.evolveum.polygon.connector.csv.CsvConnector"
    version = "2.8"
  }

  connector_configuration = {
    configurationProperties = {
      encoding        = "utf-8"
      fieldDelimiter  = ","
      filePath        = "/data/users.csv"
      nameAttribute   = "USERNAME"
      uniqueAttribute = "USERNAME"
    }
  }

  schema_handling = {
    objectType = [
      {
        attribute = [
          {
            outbound = [
              {
                expression = {
                  script = [
                    {
                      code = chomp(<<-EOT
                        // The login is the lower-cased midPoint name.
                        name?.toLowerCase()
                      EOT
                      )
                    },
                  ]
                }
                source = [
                  { path = "name" },
                ]
                strength = "strong"
              },
            ]
            ref = "ri:USERNAME"
          },
          {
            outbound = [
              {
                expression = {
                  script = [
                    {
                      code = <<-EOT
                        def n = fullName?.orig
                        return n ? "$${n} (CSV)" : null
                      EOT
                    },
                  ]
                }
                source = [
                  { path = "fullName" },
                ]
              },
            ]
            ref = "ri:DISPLAY_NAME"
          },
        ]
        default = true
        delineation = {
          objectClass = ["ri:AccountObjectClass"]
        }
        displayName = "Default account"
        focus       = { type = "UserType" }
        intent      = "default"
        kind        = "account"
      },
    ]
  }
}
