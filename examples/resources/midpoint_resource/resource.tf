# An HR feed: a read-only CSV export of employees. The connector is found by
# type and version, so the same configuration works in every environment
# (connector_oid shows the OID midPoint resolved). The containers live in
# YAML files next to this one, in midPoint naming.
resource "midpoint_resource" "hr" {
  name        = "HR"
  description = "Employees exported nightly from the HR system."

  connector = {
    type    = "com.evolveum.polygon.connector.csv.CsvConnector"
    version = "2.8"
  }

  connector_configuration = yamldecode(file("${path.module}/connector-configuration.yaml"))
  schema_handling         = yamldecode(file("${path.module}/schema-handling.yaml"))

  # The feed is read-only: midPoint never writes to it.
  capabilities = {
    create = { enabled = false }
    update = { enabled = false }
    delete = { enabled = false }
  }

  # Only the account object class is generated into the cached schema.
  schema = {
    generation_constraints = { generateObjectClass = ["ri:AccountObjectClass"] }
  }
}
