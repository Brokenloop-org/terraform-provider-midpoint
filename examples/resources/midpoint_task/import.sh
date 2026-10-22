# The import ID is the object OID. An imported task records
# initial_execution_state as suspended and is never re-created for it.
# owner and binding read back as null until they are configured, so a
# config that sets them plans one no-op update after the import.
terraform import midpoint_task.recompute_contractors 6a4c2b1e-3f0d-4b8a-9c7e-1d2f3a4b5c6d
