# An archetype for roles that grant access to Kubernetes clusters.
resource "midpoint_archetype" "cluster_role" {
  name        = "Cluster role"
  holder_type = ["RoleType"]

  archetype_policy = {
    display = {
      label       = "Cluster role"
      pluralLabel = "Cluster roles"
      icon        = { cssClass = "fa fa-cubes" }
    }
  }
}
