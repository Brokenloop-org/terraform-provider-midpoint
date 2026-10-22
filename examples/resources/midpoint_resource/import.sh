# The import ID is the resource OID. The connector reads back null (the
# resolved connector is connector_oid) and the configurationProperties
# without their "@ns", so a config plans one no-op update after the import.
# A schema definition midPoint cached is not imported.
terraform import midpoint_resource.hr 8d2c4e6f-1a3b-4c5d-9e7f-0a1b2c3d4e5f
