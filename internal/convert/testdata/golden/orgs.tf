resource "midpoint_org" "acme_corp" {
  name         = "Acme Corp"
  oid          = "1a9c5d70-3e84-4b26-9f0d-8c71b4a2e655"
  display_name = "Acme Corp"
  identifier   = "acme"
  tenant       = true
  archetype    = midpoint_archetype.tenant.oid
}

resource "midpoint_org" "acme_corp_platform" {
  name         = "Acme Corp / Platform"
  oid          = "6d0e3b81-9c47-4a52-b3e8-5a17f2c04977"
  display_name = "Acme Corp / Platform"
  identifier   = "platform"

  assignment {
    target {
      oid  = midpoint_org.acme_corp.oid
      type = "OrgType"
    }
  }

  assignment {
    extra = jsonencode({
      tenantRef = {
        oid      = midpoint_org.acme_corp.oid
        relation = "org:default"
        type     = "OrgType"
      }
    })

    target {
      oid  = midpoint_org.acme_corp.oid
      type = "OrgType"
    }
  }

  inducement {
    target {
      oid  = midpoint_role.observability_engineer.oid
      type = "RoleType"
    }
  }
}
