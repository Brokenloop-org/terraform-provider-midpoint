# A recompute task for contractors. It is created suspended (the default)
# and started from midPoint; suspending or resuming it there is not drift.
resource "midpoint_task" "recompute_contractors" {
  name        = "Recompute contractors"
  description = "Recomputes every contractor."

  activity = {
    work = {
      recomputation = {
        objects = {
          type  = "UserType"
          query = { filter = { text = "subtype = \"contractor\"" } }
        }
      }
    }
  }
}

# A recurring recompute of all users at 02:00, owned by the administrator.
# It is created suspended: resume it in midPoint to start the schedule
# (initial_execution_state = "runnable" would start it on create).
resource "midpoint_task" "nightly" {
  name                    = "Nightly recompute"
  owner                   = "00000000-0000-0000-0000-000000000002" # administrator
  initial_execution_state = "suspended"
  binding                 = "loose"

  schedule = {
    recurrence      = "recurring"
    cronLikePattern = "0 0 2 * * ?"
  }

  activity = {
    work = {
      recomputation = {
        objects = { type = "UserType" }
      }
    }
  }
}
