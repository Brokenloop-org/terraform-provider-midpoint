resource "midpoint_role" "grafana_viewer" {
  name         = "grafana / viewer"
  oid          = "c41d7e03-6b52-4f19-a8d4-0e9b3a7f5c33"
  display_name = "Grafana viewer"
  description  = "Read-only access to Grafana dashboards."
  requestable  = true
  archetype    = midpoint_archetype.application_role.oid

  inducement {
    target {
      oid  = midpoint_service.grafana.oid
      type = "ServiceType"
    }
  }
}

resource "midpoint_role" "observability_engineer" {
  name         = "Observability Engineer"
  oid          = "e7b30a96-4d85-4c21-b1f7-92a05d6c8e44"
  display_name = "Observability Engineer"
  description  = "Dashboards for engineers."
  requestable  = true
  archetype    = midpoint_archetype.platform_item.oid

  inducement {
    target {
      oid    = midpoint_role.grafana_viewer.oid
      type   = "RoleType"
      filter = "name = \"grafana / viewer\""
    }
  }
}

resource "midpoint_role" "security_reviewer" {
  name         = "Security Reviewer"
  oid          = "035d31a5-7403-4aff-86a8-0f941e5a6403"
  display_name = "Security Reviewer"
  description  = "Provides access to security office tooling."
  risk_level   = "high"
  requestable  = false
  archetype    = midpoint_archetype.platform_item.oid

  inducement {
    target {
      type   = "RoleType"
      filter = "name = \"vault / operator\""
    }
  }
}

resource "midpoint_role" "vault_operator" {
  name         = "vault / operator"
  oid          = "9a5bdeed-fa50-4a7d-bebc-604e5ddf0298"
  display_name = "Vault operator"
  description  = "Operate the Vault cluster."
  risk_level   = "high"
  requestable  = true
  archetype    = midpoint_archetype.application_role.oid

  inducement {
    target {
      oid  = midpoint_service.vault.oid
      type = "ServiceType"
    }
  }
}
