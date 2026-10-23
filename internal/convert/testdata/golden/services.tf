resource "midpoint_service" "grafana" {
  name         = "Grafana"
  oid          = "8c2e90b4-17d3-4a65-8f0b-d3a6e15c7b22"
  display_name = "Grafana"
  description  = "Dashboards and alerting."
  identifier   = "grafana"
  archetype    = midpoint_archetype.platform_item.oid

  inducement {
    construction {
      resource = midpoint_resource.users_csv.oid
      kind     = "account"
      intent   = "default"

      body = jsonencode({
        attribute = [
          {
            outbound = [
              {
                expression = {
                  script = [
                    {
                      code = chomp(<<-EOT
                        // Grafana users join the viewers group of their team.
                        return "grafana-viewers-$${basic.stringify(costCenter)}"
                      EOT
                      )
                    },
                  ]
                }
                strength = "strong"
              },
            ]
            ref = "ri:GROUPS"
          },
        ]
      })
    }
  }
}

resource "midpoint_service" "vault" {
  name         = "Vault"
  oid          = "3f6a1c52-8d0e-4b7a-9a41-5c2e7d9b0a11"
  display_name = "Vault"
  description  = "Secrets management platform."
  identifier   = "vault"
  archetype    = midpoint_archetype.platform_item.oid
}
