---
page_title: "Getting role changes to reach members"
subcategory: ""
description: |-
  Recompute the members of a role, archetype or org automatically when its inducements change, using a midPoint policy rule.
---

# Getting role changes to reach members

When you change what a role, archetype or org induces, midPoint updates the
object itself but not the users who already hold it. They get the new access
only when each of them is next recomputed. This is how midPoint works, with or
without this provider, and `terraform apply` succeeds without doing it.

midPoint can do this for you with a policy rule that recomputes the members when
the inducements change. This guide sets up that rule for the objects you choose,
and shows a one-off task for bringing existing members up to date.

The setup and the task below were verified on midPoint 4.9, 4.10 and 4.11 by the
`TestAccMemberRecompute_*` acceptance tests. See midPoint's
[Linked Objects](https://docs.evolveum.com/midpoint/reference/support-4.10/synchronization/linked-objects/)
for the mechanism.

## Set up the rule

Put the rule on a metarole, then assign the metarole to each object that should
pass its changes on. Objects without the assignment are not affected.

```terraform
resource "midpoint_role" "recompute_members" {
  name = "recompute-members"

  inducement {
    policy_rule = jsonencode({
      name = "recompute-members-on-inducement-change"
      policyConstraints = {
        modification = { item = "inducement" }
      }
      policyActions = {
        scriptExecution = {
          object = { linkSource = { description = "Members of the changed object" } }
          executeScript = {
            recompute = {
              triggered = {
                "@type"   = "c:TriggerCreationType"
                fireAfter = "PT5M"
              }
            }
          }
        }
      }
    })
  }
}

resource "midpoint_role" "dev_k8s_admin" {
  name    = "dev-k8s / admin"
  options = [] # Required: the rule does not run on a raw write.

  assignment {
    target {
      oid  = midpoint_role.recompute_members.oid
      type = "RoleType"
    }
  }
}
```

The same `assignment` and `options` work on `midpoint_archetype` and
`midpoint_org`.

Keep the rule in the form shown, or the plan never settles:

- `modification`, `item` and `scriptExecution` are single values, not lists.
- `linkSource` has a description. midPoint stores an empty `linkSource`, but the
  provider reads an empty value as absent and keeps planning to add it.
- `triggered` carries the `@type` that midPoint adds when it stores the value.

## Turn off raw writes

`midpoint_role` and `midpoint_archetype` are written with the `raw` option by
default, and a raw write skips policy rules. If you leave it on, the apply
succeeds and the rule silently never runs. `midpoint_org` writes are not raw by
default.

Set `options = []` on each object, or turn raw off for the whole type:

```terraform
provider "midpoint" {
  default_options = {
    role      = []
    archetype = []
  }
}
```

Turning raw off is not enough by itself: without the rule, members stay as they
were. It also changes how those writes behave:

- Object templates, policies and mappings now run on those objects, so check any
  that match them.
- Approval policies can hold the write. See
  [Writes held for approval](https://github.com/Brokenloop-org/terraform-provider-midpoint#writes-held-for-approval).

A role that was first written raw can move to this setup in place: apply
`options = []` and the assignment, and later inducement changes run the rule.

## Let the rule run under midPoint 4.11

A midPoint 4.11 repository created from scratch runs the expressions of roles,
orgs and archetypes under the `safe` expression profile, which denies bulk
actions such as `recompute`. The rule then fails every apply that changes the
inducements of an object with it:

```
midPoint returned HTTP 250: Couldn't execute script: Access to action
'recompute' denied (applied expression profile 'safe', actions profile
'prohibitive')
```

To check your server, look in the system configuration for a
`defaultObjectPolicyConfiguration` entry with type `RoleType` and an
`expressionProfile`. A repository upgraded from 4.10 or earlier keeps its
system configuration and has none, so it needs no change. Neither do 4.9 and
4.10, which do not define the profile used below; referencing it there makes
the rule fail with "No expression profile with identifier 'configuration'".

Where it applies, put the metarole under an archetype that runs it with the
`configuration` profile, which 4.11 defines, and write the metarole without raw
so that midPoint records the archetype:

```terraform
resource "midpoint_archetype" "trusted_policy" {
  name = "trusted-policy"

  archetype_policy = {
    expressionProfile = "configuration"
  }
}

resource "midpoint_role" "recompute_members" {
  name    = "recompute-members"
  options = []

  assignment {
    target {
      oid  = midpoint_archetype.trusted_policy.oid
      type = "ArchetypeType"
    }
  }

  inducement {
    policy_rule = jsonencode({
      # The rule shown above, unchanged.
    })
  }
}
```

This gives the metarole more power than 4.11 gives roles by default: the
`configuration` profile allows every bulk action and privilege elevation, for
all expressions in the metarole. Keep who can change the metarole and the
archetype as narrow as for the system configuration. See midPoint's
[expression profiles](https://docs.evolveum.com/midpoint/reference/expressions/expressions/profiles/).

## What to expect

- **An apply can start a recompute.** Once an object has the rule, any apply that
  changes its inducements recomputes its members. Other changes to the object do
  not.
- **The recompute runs later.** `fireAfter` delays it, and midPoint's Trigger
  Scanner task, which by default runs every 5 minutes, then has to pick it up. So
  `PT5M` means up to about 10 minutes. Changes made during the delay share one
  recompute.
- **It reconciles.** The recompute reconciles each member: midPoint fetches their
  accounts from the connected systems, compares them and updates them as needed.
  Expect load on those systems, and changes there, in proportion to the number of
  members.
- **It finds members through `roleMembershipRef`.** midPoint's docs warn that this
  item can be out of date. A user whose membership was never computed, such as
  one assigned the role by a raw write, is not found.
- **The apply does not report the outcome.** Terraform has finished by the time
  the recompute runs, so check failures in midPoint.

## Bring existing members up to date

The rule reacts only to later changes. For a change made before the rule was in
place, or for members it did not reach, run a one-off recompute task:

```terraform
resource "midpoint_task" "recompute_dev_k8s_admin" {
  name                    = "Recompute dev-k8s / admin members"
  initial_execution_state = "runnable"

  activity = {
    work = {
      recomputation = {
        objects = {
          type = "UserType"
          query = {
            filter = {
              text = "assignment matches (targetRef matches (oid = '${midpoint_role.dev_k8s_admin.oid}'))"
            }
          }
        }
        executionOptions = { reconcile = false }
      }
    }
  }
}
```

- **Scope:** the filter matches users assigned the role directly, not users who
  inherit it through another role or org. `type` limits the task to users. If you
  leave it out, midPoint uses `FocusType`, which also covers roles, orgs and
  services.
- **Reconcile:** without `executionOptions`, a recompute task reconciles. Setting
  `reconcile = false` limits this run to re-evaluating the members.
- **Running it again:** the task runs when it is created. To run it again, use
  `terraform apply -replace=midpoint_task.recompute_dev_k8s_admin`, or start it
  in midPoint. Remove the resource when you no longer need it.

midPoint warns that some recompute settings can damage data, so try the task on
a non-production system first. See its
[recompute task](https://docs.evolveum.com/midpoint/reference/support-4.10/tasks/recompute-task/)
documentation.
