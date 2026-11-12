---
page_title: "midpoint Provider"
description: |-
  Manages Evolveum midPoint objects through the midPoint REST API.
---

# midpoint Provider

Manages Evolveum midPoint objects through the midPoint REST API.

The provider manages midPoint configuration objects (services, roles, orgs,
archetypes, object templates, marks, tasks, connector resources, and any other
type through `midpoint_object`) as Terraform resources. It compares what
midPoint returns strictly with the configuration, so a change made in the
midPoint GUI shows as drift, and it writes only the changed items as narrow
item deltas.

A change to a role, archetype or org does not reach the users who already hold
it until they are recomputed. See
[Getting role changes to reach members](guides/role-changes-reaching-members.md).

To keep a connector password out of Terraform state, give it as a reference to
a midPoint secrets provider. See [Connector secrets](guides/connector-secrets.md).

Safety rules that apply to every resource:

- Creating an object whose `oid` already exists fails unless `adopt_existing = true`.
  An adopted object takes the whole configuration: items it does not
  configure are removed on the first apply, and the plan warning lists the
  changes.
- Deleting a role, service, org or archetype that still has members, or a
  resource that still has shadows, fails unless `force_destroy = true`.
- Deleting an object adopted with `adopt_existing = true` fails unless
  `force_destroy = true`; remove it from state with a `removed` block
  (`lifecycle { destroy = false }`) to stop managing it.
- `overwrite` is never sent unless it is listed in a resource's `options`.

## Example Usage

```terraform
terraform {
  required_providers {
    midpoint = {
      source = "brokenloop-org/midpoint"
    }
  }
}

variable "midpoint_username" {
  type = string
}

variable "midpoint_password" {
  type      = string
  sensitive = true
}

provider "midpoint" {
  url      = "https://midpoint.example.com" # or MIDPOINT_URL
  username = var.midpoint_username          # or MIDPOINT_USERNAME
  password = var.midpoint_password          # or MIDPOINT_PASSWORD

  # Optional: a private CA instead of skipping verification (insecure = true).
  # ca_bundle = file("${path.module}/ca.pem")

  # Optional: the execution options of every write of a resource type,
  # here none for roles (instead of the built-in isImport and raw).
  # default_options = {
  #   role = []
  # }

  # Optional: report a partial error (HTTP 250) as a warning instead of failing
  # the apply and tainting a newly created object.
  # on_partial_error = "warning"
}
```

## Environment variables

Every attribute except `ca_bundle`, `default_options`, `fail_on_unapplied_write`,
`on_partial_error` and `on_invalid_item` can come from the environment. An attribute set in the
configuration takes precedence. `url`, `username` and `password` are required,
from either source.

| Attribute | Environment variable | Notes |
|---|---|---|
| `url` | `MIDPOINT_URL` | Base URL without `/midpoint`, e.g. `https://midpoint.example.com`. |
| `username` | `MIDPOINT_USERNAME` | HTTP basic authentication. |
| `password` | `MIDPOINT_PASSWORD` | HTTP basic authentication. |
| `insecure` | `MIDPOINT_INSECURE` | `true` skips TLS verification (development only). |

## Invalid items

At plan time the provider checks free-form attributes against the schema of the
server's midPoint version. An unknown key or an impossible value fails the plan,
with its path. `on_invalid_item = "warning"` downgrades this to a warning. See
[midPoint compatibility](https://github.com/Brokenloop-org/terraform-provider-midpoint/blob/main/docs/compatibility.md#invalid-item-checks)
for what is checked and what is not.

Upgrading from v0.2? See the
[upgrade guide](https://github.com/Brokenloop-org/terraform-provider-midpoint/blob/main/docs/upgrading.md).

## Partial errors

midPoint answers a write with HTTP 250 when the object is saved but part of the
operation failed, typically provisioning an account to a resource. By default,
`on_partial_error = "error"`, that fails the apply. A failed create keeps the
object in state and marks it tainted, so the next apply replaces it, which also
removes any accounts it already has on other resources. Run `terraform untaint`
to keep it instead. A failed update does not taint, but its change is saved: a
later apply does not send it again, so it succeeds while nothing retries what
failed.

With `on_partial_error = "warning"` a create or update reports a warning and the
apply carries on, leaving midPoint to complete what failed, for example by a
reconciliation. A partial error on a delete is always an error, because a
warning would drop the object from state whether or not it is gone.

## Execution options

Each resource sends midPoint execution options on create and update. A
resource's `options` attribute wins over the provider's `default_options`
entry for its type, which wins over the built-in defaults below. A list given
in `options` or `default_options` is sent on both create and update; an empty
list sends no options.

| `default_options` key | Create | Update |
|---|---|---|
| `service`, `role`, `archetype`, `object_template` | `isImport`, `raw` | `raw` |
| `org`, `mark`, `task`, `object` | `isImport` | none |
| `resource` | `isImport`, `reevaluateSearchFilters` | `reevaluateSearchFilters` |

`reevaluateSearchFilters` is added to any write that sends a reference with
a filter and no oid.

## Writes held for approval

midPoint's REST API does not say when an approval policy holds a write, so the
provider reports what it observes and names approval as a possible cause. An
update warns if the object read back differs from the configuration, and a
delete warns if the object still exists and stops tracking it. A create that
returns no object ID fails. `fail_on_unapplied_write` fails the update or delete
instead, which also catches values midPoint normalized.

The `raw` execution option skips approval, but also skips the rest of midPoint's
processing, such as updating `parentOrgRef`, and does not cancel approvals
already pending. See the
[README](https://github.com/Brokenloop-org/terraform-provider-midpoint#writes-held-for-approval)
before using it for this.

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `ca_bundle` (String) PEM-encoded CA certificates used to verify the midPoint server certificate, e.g. file("ca.pem"). When set they replace the system trust store.
- `default_options` (Map of List of String) Default midPoint execution options per resource type, keyed by resource short name (service, role, org, archetype, object_template, mark, task, resource, object). A resource's own options attribute takes precedence.
- `fail_on_unapplied_write` (Boolean) Fail an update when the object read back differs from the configuration, and a delete when the object still exists. Both are warnings by default. A write held for approval looks like this, but so does a value midPoint normalized, which also fails.
- `insecure` (Boolean) Skip TLS certificate verification (development only). May also be set with MIDPOINT_INSECURE. Defaults to false.
- `on_invalid_item` (String) What to do when the configuration of a resource holds a key or a value that midPoint's schema does not accept, such as a misspelled item name or an enum value that does not exist. It is checked at plan time against the schema of the server's midPoint version (4.9, 4.10 and 4.11 are known). "error" (the default) fails the plan; "warning" reports it and carries on, for an item your midPoint accepts although its schema does not list it. Other versions are not checked.
- `on_partial_error` (String) What to do when midPoint answers a create or update with a partial error (HTTP 250): the object is saved, but part of the operation failed, such as provisioning an account to a resource. "error" (the default) fails the apply; an object just created is kept in state and marked tainted, so the next apply replaces it. "warning" reports it and carries on, leaving midPoint to complete what failed. A partial error on a delete is always an error. The same applies when midPoint answers a create with a server error (HTTP 5xx, such as 502 for a fatal provisioning error) but saved the object: the provider finds it by the OID it sent.
- `password` (String, Sensitive) Password for HTTP basic authentication. May also be set with MIDPOINT_PASSWORD.
- `url` (String) Base URL of the midPoint server, e.g. https://midpoint.example.com; the provider appends /midpoint/ws/rest. May also be set with MIDPOINT_URL.
- `username` (String) Username for HTTP basic authentication. May also be set with MIDPOINT_USERNAME.
