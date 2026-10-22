# A complete configuration: a tenant with a department, a service with the
# roles that grant access to it, a business role that bundles them, and a
# recompute task. Apply it to a test server with:
#
#   terraform apply -var midpoint_url=http://localhost:8080 -var midpoint_password=...

provider "midpoint" {
  url      = var.midpoint_url
  username = var.midpoint_username
  password = var.midpoint_password
}

# Built-in archetypes, looked up by name instead of hard-coded OIDs.
data "midpoint_object" "application" {
  type = "archetypes"
  name = "Application"
}

data "midpoint_object" "application_role" {
  type = "archetypes"
  name = "Application role"
}

# The tenant: an org with tenant = true.
resource "midpoint_org" "acme" {
  name         = "acme"
  display_name = "ACME Corporation"
  tenant       = true
}

# The service (an application).
resource "midpoint_service" "dev_k8s" {
  name         = "dev-k8s"
  display_name = "Dev Kubernetes cluster"
  description  = "Kubernetes cluster for development."
  archetype    = data.midpoint_object.application.oid
}

# The service roles: each one induces the service.
resource "midpoint_role" "dev_k8s" {
  for_each = var.service_roles

  name         = "dev-k8s / ${each.key}"
  display_name = each.value.display_name
  identifier   = each.key
  risk_level   = each.value.risk_level
  archetype    = data.midpoint_object.application_role.oid

  inducement {
    target {
      oid  = midpoint_service.dev_k8s.oid
      type = "ServiceType"
    }
  }
}

# A business role. The first inducement names its target by OID; the second
# finds its target with a midPoint query, which Terraform cannot see, so the
# role it selects is ordered before this one with depends_on.
resource "midpoint_role" "platform_engineer" {
  name        = "platform-engineer"
  description = "Everything a platform engineer needs."

  inducement {
    target {
      oid  = midpoint_role.dev_k8s["admin"].oid
      type = "RoleType"
    }
  }

  inducement {
    target {
      type   = "RoleType"
      filter = "name = \"dev-k8s / viewer\""
    }
  }

  depends_on = [midpoint_role.dev_k8s]
}

# A department of the tenant. Its members get the viewer role and the
# business role.
resource "midpoint_org" "acme_platform" {
  name         = "acme-platform"
  display_name = "ACME platform team"

  assignment {
    target {
      oid  = midpoint_org.acme.oid
      type = "OrgType"
    }
  }

  inducement {
    target {
      oid  = midpoint_role.dev_k8s["viewer"].oid
      type = "RoleType"
    }
    # Free-form items inside a block are JSON text.
    extra = jsonencode({ description = "Read access for the whole team." })
  }

  inducement {
    target {
      oid  = midpoint_role.platform_engineer.oid
      type = "RoleType"
    }
  }
}

# A recompute task for the department's members. It is created suspended and
# started from midPoint; suspending or resuming it there is not drift.
resource "midpoint_task" "recompute_platform" {
  name                    = "Recompute ACME platform team"
  initial_execution_state = "suspended"

  activity = {
    work = {
      recomputation = {
        objects = {
          type  = "UserType"
          query = { filter = { text = "parentOrgRef matches (oid = \"${midpoint_org.acme_platform.oid}\")" } }
        }
      }
    }
  }
}
