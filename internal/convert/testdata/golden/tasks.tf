resource "midpoint_task" "users_csv_reconciliation" {
  name                    = "Users CSV reconciliation"
  oid                     = "214ed436-8d6e-4a57-b63b-f87bb8305a34"
  owner                   = data.midpoint_object.user_2.oid
  binding                 = "loose"
  initial_execution_state = "suspended"

  activity = {
    work = {
      reconciliation = {
        resourceObjects = {
          intent = "default"
          kind   = "account"
          resourceRef = {
            oid      = midpoint_resource.users_csv.oid
            relation = "org:default"
            type     = "ResourceType"
          }
        }
      }
    }
  }
}
