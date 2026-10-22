# Find the CSV connector installed on this midPoint server.
data "midpoint_connector" "csv" {
  connector_type = "com.evolveum.polygon.connector.csv.CsvConnector"
}

# Pin a version when the server has several versions of a connector type.
data "midpoint_connector" "csv_2_8" {
  connector_type    = "com.evolveum.polygon.connector.csv.CsvConnector"
  connector_version = "2.8"
}

output "csv_connector" {
  value = {
    oid     = data.midpoint_connector.csv.oid
    name    = data.midpoint_connector.csv.name
    version = data.midpoint_connector.csv.connector_version
  }
}
