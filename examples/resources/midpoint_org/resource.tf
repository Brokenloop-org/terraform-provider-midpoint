# A tenant.
resource "midpoint_org" "acme" {
  name         = "acme"
  display_name = "ACME Corporation"
  tenant       = true
}

# A department of the tenant.
resource "midpoint_org" "acme_it" {
  name = "acme-it"

  assignment {
    target {
      oid  = midpoint_org.acme.oid
      type = "OrgType"
    }
  }
}
