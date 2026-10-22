# The service the roles below grant access to.
resource "midpoint_service" "dev_k8s" {
  name      = "dev-k8s"
  archetype = "00000000-0000-0000-0000-000000000329" # Application
}

# An application role granting access to the service.
resource "midpoint_role" "dev_k8s_admin" {
  name         = "dev-k8s / admin"
  display_name = "Dev cluster / admin"
  description  = "Administrator access to the dev cluster."
  identifier   = "admin"
  risk_level   = "high"
  archetype    = "00000000-0000-0000-0000-000000000328" # Application role

  inducement {
    target {
      oid  = midpoint_service.dev_k8s.oid
      type = "ServiceType"
    }
  }
}

# A business role: induces another role found by a midPoint query, and
# creates an LDAP account for its members' members.
resource "midpoint_role" "platform_team" {
  name = "platform-team"

  inducement {
    target {
      type   = "RoleType"
      filter = "name = \"vault / operator\""
    }
  }

  inducement {
    order = 2
    construction {
      resource = "c5a3191b-89c7-479e-82b9-6093c60566f2"
      kind     = "account"
      intent   = "default"
    }
    condition = jsonencode(yamldecode(file("${path.module}/only-employees.yaml")))
  }

  extension = {
    costCenter = "CC-123"
  }
}
