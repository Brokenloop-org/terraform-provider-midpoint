# Terraform provider for Evolveum midPoint

`terraform-provider-midpoint` manages midPoint configuration objects (services,
roles, orgs, archetypes, object templates, marks, tasks, connector resources,
and any other type) in HCL through the midPoint REST API.

- Typed resources for the common object kinds, with Terraform references
  between objects instead of hard-coded OIDs.
- Strict drift detection: everything midPoint returns that is not known to be
  server-managed is compared, so a change made in the midPoint GUI shows in the
  plan. No `ignore_changes` lists are needed.
- Narrow writes: an update is one `PATCH` with one item delta per changed item;
  the object is never resent whole.
- Safe by default: no silent overwrite on create, no delete of an object that
  still has members or shadows.
- Checked at plan: a misspelled key or an impossible value in a free-form
  attribute fails `terraform plan`, using the schema of your midPoint version.

Tested against midPoint 4.9, 4.10 and 4.11 (acceptance suite on a dockerized
server); [`docs/compatibility.md`](docs/compatibility.md) has the results.
Requires Terraform 1.5 or later.

Upgrading from v0.2.0 or earlier? v0.3.0 has breaking changes: see
[`docs/upgrading.md`](docs/upgrading.md).

Generated reference documentation is in [`docs/`](docs/index.md); runnable
examples are in [`examples/`](examples/).

## Install

### From the registry

```hcl
terraform {
  required_providers {
    midpoint = {
      source = "brokenloop-org/midpoint"
    }
  }
}
```

### Local build (dev override)

Until a release is published, or to test a change, build the provider and
point Terraform at the binary with a `dev_overrides` CLI configuration:

```sh
go build -o ./dist/terraform-provider-midpoint .

cat > dist/terraformrc <<EOF
provider_installation {
  dev_overrides {
    "brokenloop-org/midpoint" = "$PWD/dist"
  }
  direct {}
}
EOF

export TF_CLI_CONFIG_FILE="$PWD/dist/terraformrc"
```

With the override in place, `terraform plan` and `apply` use the local binary
(Terraform prints a warning saying so). `make validate-examples` does exactly
this to validate every example.

## Provider configuration

```hcl
provider "midpoint" {
  url      = "https://midpoint.example.com" # or MIDPOINT_URL
  username = var.midpoint_username          # or MIDPOINT_USERNAME
  password = var.midpoint_password          # or MIDPOINT_PASSWORD
}
```

| Attribute | Environment variable | Required | Description |
|---|---|---|---|
| `url` | `MIDPOINT_URL` | yes | Server base URL, e.g. `https://midpoint.example.com`. The provider appends `/midpoint/ws/rest`. |
| `username` | `MIDPOINT_USERNAME` | yes | User for HTTP basic authentication. |
| `password` | `MIDPOINT_PASSWORD` | yes | Password (sensitive). |
| `insecure` | `MIDPOINT_INSECURE` | no | `true` skips TLS verification (development only). Default `false`. |
| `ca_bundle` | none | no | PEM CA certificates, e.g. `file("ca.pem")`. When set they replace the system trust store. |
| `default_options` | none | no | Execution options per resource type, keyed by `service`, `role`, `org`, `archetype`, `object_template`, `mark`, `task`, `resource`, `object`. See [Execution options](#execution-options). |
| `fail_on_unapplied_write` | none | no | `true` fails an update when the object read back differs from the configuration, and a delete when the object still exists. Default `false` (warnings). See [Writes held for approval](#writes-held-for-approval). |
| `on_partial_error` | none | no | `"error"` (the default) fails the apply when midPoint answers a create or update with HTTP 250; `"warning"` reports it and carries on. See [Partial errors](#partial-errors). |
| `on_invalid_item` | none | no | `"error"` (the default) fails the plan on a key or value midPoint's schema does not accept; `"warning"` reports it and carries on. See [Invalid items](#invalid-items). |

A value set in the configuration wins over the environment variable. At
configure time the provider reads the server version, for its logs and to pick the schema
table of the invalid item checks. A failure there is a warning, not an error,
and the checks are skipped.

## Resources and data sources

| Resource | midPoint type | REST type | Import ID |
|---|---|---|---|
| [`midpoint_service`](docs/resources/service.md) | ServiceType | `services` | OID |
| [`midpoint_role`](docs/resources/role.md) | RoleType | `roles` | OID |
| [`midpoint_org`](docs/resources/org.md) | OrgType | `orgs` | OID |
| [`midpoint_archetype`](docs/resources/archetype.md) | ArchetypeType | `archetypes` | OID |
| [`midpoint_object_template`](docs/resources/object_template.md) | ObjectTemplateType | `objectTemplates` | OID |
| [`midpoint_mark`](docs/resources/mark.md) | MarkType | `marks` | OID |
| [`midpoint_task`](docs/resources/task.md) | TaskType | `tasks` | OID |
| [`midpoint_resource`](docs/resources/resource.md) | ResourceType | `resources` | OID |
| [`midpoint_object`](docs/resources/object.md) | any | any, set in `type` | `<type>/<oid>`, e.g. `functionLibraries/7d4e...` |

- Tenants are `midpoint_org` with `tenant = true`.
- Simple, business and service roles and metaroles are all `midpoint_role`;
  the archetype tells them apart.
- `midpoint_object` manages any type through its free-form `body`, which is
  authoritative (items left out of `body` are removed, except server-managed
  ones).

| Data source | Looks up | Exposes |
|---|---|---|
| [`midpoint_object`](docs/data-sources/object.md) | one object by `type` and `name`, or by `oid` | `oid`, `name`, `body` (canonical, protected strings removed) |
| [`midpoint_connector`](docs/data-sources/connector.md) | a connector by `connector_type` and optional `connector_version` | `oid`, `name`, `connector_version` |

Both data sources fail when nothing matches or more than one object matches.

## Safety rules

| Rule | Behaviour |
|---|---|
| No silent overwrite | Creating an object whose `oid` already exists fails and writes nothing; the error says to import it. |
| `adopt_existing = true` | The existing object is adopted into state instead, and the configuration is applied to it as an update. The configuration must describe the whole object: items midPoint holds that are not configured (other than server-managed ones) are removed on the first apply. The plan warning lists the changes the update will send. Meant for midPoint's built-in objects (`00000000-0000-0000-0000-*`). Default `false`. See [Adopting a built-in](#adopting-a-built-in). |
| Adopted objects are not destroyed | While a resource has `adopt_existing = true` in state, destroying it (or replacing it) fails and sends nothing: "<type>/<oid> was adopted from midPoint; refusing to delete it". To stop managing it, remove it from state with a `removed { lifecycle { destroy = false } }` block; set `force_destroy = true` to delete it anyway. |
| `force_destroy = true` | Required to delete a service, role, org or archetype that still has members (objects assigned to it or holding it in `roleMembershipRef`), or a resource that still has shadows. Without it the delete fails and names the first few. `midpoint_object` applies the same check for `roles`, `services`, `orgs`, `archetypes` and `resources`. Marks, tasks and object templates are deleted without a check. It also lifts the refusal to delete an adopted object. Default `false`. |
| `overwrite` never sent | Only when listed in a resource's `options`. |
| Read-back is not fatal after a create | Every write is followed by a read. If that read fails after a successful create or adoption, it is a warning: the object stays in state (no taint, no replace) and the next refresh reconciles it. |
| Tasks are not started or stopped | `initial_execution_state` is written on create only; see [Drift semantics](#drift-semantics). |

### Adopting a built-in

```hcl
# Customize the built-in End user role. body must hold the whole role, as
# exported from this server: anything it leaves out is removed on the first
# apply.
resource "midpoint_object" "end_user" {
  type           = "roles"
  oid            = "00000000-0000-0000-0000-000000000008"
  adopt_existing = true
  body           = yamldecode(file("${path.module}/end-user-role.yaml"))
}
```

Export the object from the server you will apply to (its content differs
between midPoint versions), for example the `body` of a `data.midpoint_object`
for that oid written out with `yamlencode`, then edit it. The plan lists what
the adoption will change, without values:

```
Warning: Existing object will be adopted

midpoint_object roles/00000000-0000-0000-0000-000000000008 exists and will be
adopted; applying the configuration will send these changes:
  replace description
  replace riskLevel (removes it)
```

The list shows at most 20 changes, then "and N more". When a configured
value is known only during apply, the warning says that the changes cannot
be listed.

An adopted object is never deleted by a destroy unless `force_destroy = true`.
To stop managing it, drop it from state and leave it in midPoint:

```hcl
removed {
  from = midpoint_object.end_user

  lifecycle {
    destroy = false
  }
}
```

### Execution options

Each resource sends midPoint execution options on create and update.
Precedence: the resource's `options` attribute, then the provider's
`default_options` entry for the type, then the built-in defaults. A list from
`options` or `default_options` is sent on both create and update; an empty
list sends no options.

| Type (`default_options` key) | Create | Update |
|---|---|---|
| `service`, `role`, `archetype`, `object_template` | `isImport`, `raw` | `raw` |
| `org`, `mark`, `task` | `isImport` | none |
| `resource` | `isImport`, `reevaluateSearchFilters` | `reevaluateSearchFilters` |
| `object` (any type) | `isImport` | none |

- `reevaluateSearchFilters` is added automatically to any write that sends a
  reference with a filter and no oid (midPoint rejects it otherwise).
- Reads never use `raw`. `midpoint_resource` (and `midpoint_object` with
  `type = "resources"`) reads with `noFetch`, so a refresh never calls the
  connector.

## Drift semantics

- **Strict two-way compare.** The configured object and the server object are
  brought to a canonical form and compared item by item in both directions.
  An item midPoint holds that the configuration does not is drift, and the
  plan removes it. There are no user-facing ignore lists.
- **Server-managed items** are dropped before the compare and never enter
  state. The table lives in `internal/normalize/rules.go`, one documented row
  per item:

  | Scope | Server-managed items |
  |---|---|
  | All types | `@ns`, root `@type`, `@incomplete`, `metadata`, `@metadata`, `cachingMetadata`, `version`, `iteration`, `iterationToken`, `operationExecution`, `fetchResult`, `diagnosticInformation`, `trigger`, `behavior`, `lensContext`, `roleMembershipRef`, `archetypeRef`, `linkRef`, `parentOrgRef`, `tenantRef`, `delegatedRef`, `effectiveMarkRef`, `targetName` in references, `policySituation`, `triggeredPolicyRule`, computed `activation` items (`effectiveStatus`, `validityStatus`, `*Timestamp`, `disableReason`), container `@id` |
  | Resources | `operationalState`, `operationalStateHistory`, `capabilities/native`, `capabilities/cachingMetadata`, `schema/cachingMetadata` |
  | Tasks | `taskIdentifier`, `executionState`, `schedulingState`, `stateBeforeSuspend`, `schedulingStateBeforeSuspend`, `node`, `nodeAsObserved`, run timestamps, `result`, `resultStatus`, `progress`, `expectedTotal`, `operationStats`, `activityState`, `affectedObjects`, `unpauseAction` |
  | Function libraries | `function/parameter/@type` |

  Exemptions: these match a broader rule above but are configuration, so
  they are compared:

  | Item | Why it is configuration |
  |---|---|
  | `behavior` on reports | `ReportBehaviorType` (direction, import options); the `behavior` rule targets `FocusType.behavior`. |
  | `identityProvider/metadata` (any depth) | The IdP metadata of a SAML2 authentication module in a security policy; the `metadata` rule targets `MetadataType`. |
  | resource `schema/definition/@ns` | The XML Schema namespace is part of the schema definition: midPoint rejects a definition written without it. The `@ns` rule targets serialization annotations. |
- **Compared only when configured.** midPoint fills these in when the
  configuration leaves them out. The server value is drift only once the
  configuration sets the item:

  | Item | Why |
  |---|---|
  | root `oid` | midPoint generates it. |
  | task `owner` (`ownerRef`) | midPoint makes the calling user the owner. |
  | task `binding` | midPoint defaults it to `loose`. |
  | task `assignment` | midPoint assigns a task its activity's archetype on the first run. A hand-added task assignment is not drift unless the task configures assignments. |
  | resource `schema.definition` | midPoint caches the connector schema there on a connection test. Removing `definition` from the configuration leaves the server's schema in place; it is not deleted. |
  | resource `cacheInvalidationTimestamp` | midPoint 4.11 stamps it on a connection test or an import task, and it can be set by hand. See [`docs/compatibility.md`](docs/compatibility.md#411-differences-found). |

- **Task execution state** (`executionState`, `schedulingState`) is
  server-managed. Suspending or resuming a task in midPoint is not drift, and
  an apply never restarts a task.
- **After a write**, the provider reads the object back. If the object differs
  from the configuration, a warning names the differing items, state keeps the
  configured values, and the next plan shows the difference. When the
  configuration and midPoint differ but no item delta can express the
  difference, the apply sends nothing and fails, naming the items. See
  [Writes held for approval](#writes-held-for-approval).
- **Empty lists** in midPoint are the same as a missing item. An object written
  with an explicit `subtype: []`, say by a script or a JSON import, keeps it and
  returns it on every read, and no delta removes it. The provider does not show
  it as drift and leaves it in place.
- **Multi-valued containers** (assignments, inducements, schema handling
  object types, attributes, mappings) are matched by content, not by
  position: assignments by target, construction (`resource`, `kind`, `intent`)
  and `order`; object types by `kind`/`intent`; attributes by `ref`. Order is
  not significant. A change inside a matched value (a construction `body`, a
  `condition`, an `extra` item) is a narrow delta on that value. A change to
  an identity field of an assignment or inducement (`target`, the
  construction `resource`, `kind` or `intent`, or `order`) is a different
  value: it is applied as a delete of the old value and an add of the new
  one, so the container id changes.

### Invalid items

At plan time the provider checks free-form attributes against the schema of the
server's midPoint version. An unknown key or an impossible value fails the plan,
with its path. `on_invalid_item = "warning"` in the provider block downgrades this
to a warning. Set it when your midPoint accepts an item the schema does not list.
[`docs/compatibility.md`](docs/compatibility.md#invalid-item-checks) says what is
checked and what is not.

### Partial errors

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

### Writes held for approval

midPoint's REST API does not say when an approval policy holds a write, so the
provider reports what it observes and names approval as a possible cause:

| Write | midPoint answers | The provider |
|---|---|---|
| Update | `204`, as for a completed update | Warns if the object read back differs from the configuration. The cause can also be a value midPoint normalized. |
| Delete | `204`, as for a completed delete | Warns if the object still exists, and stops tracking it. If midPoint keeps the object, it stays there unmanaged. |
| Create | `202` with no `Location` | Fails, because there is no object ID to record. |

Check midPoint to find out which it is. While a change waits on approval, the
next plan shows it again, and applying it again can open another approval
request.

`fail_on_unapplied_write = true` in the provider block fails the update or
delete instead of warning, and keeps the prior state. It compares what was
written with what was read back, so it also fails on a value midPoint
normalized, and a read-back difference right after a create or adoption stays
a warning.

#### Bypassing approval with `raw`

To skip approval for a resource type, set `raw` in its options:

```hcl
provider "midpoint" {
  default_options = {
    org = ["isImport", "raw"]
  }
}
```

`raw` skips approval and the rest of midPoint's processing, and needs the
`rawOperation` authorization. It does not make the configuration the only
source of truth:

- **Computed items are not updated.** Verified on midPoint 4.9: assigning an org
  to a parent with `raw` leaves `parentOrgRef` and `roleMembershipRef` unset, so
  the org is not in the tree until midPoint recomputes it.
- **Pending approvals still apply.** Verified on midPoint 4.9: approving a request
  opened before a `raw` write overwrites the value that write set. Close open
  approval cases for the object first.
- **Policies, object templates and mappings do not run**, and a change may not
  reach connected resources until something recomputes the object.
- **Approvers never see the change.** Check that your audit requirements allow
  this, and scope the account's `rawOperation` authorization narrowly.

## HCL conventions

| Where | Form | Example |
|---|---|---|
| Typed attributes | snake_case HCL attributes | `display_name = "Dev cluster"` |
| Root-level free-form attributes (`extra`, `extension`, `body`, `authorization`, `archetype_policy`, the resource containers, task `schedule` and `activity`, ...) | HCL values in midPoint JSON naming (camelCase, as in the REST API) | `extra = yamldecode(file("${path.module}/role-extra.yaml"))` |
| Free-form fields inside repeatable blocks (`assignment`/`inducement` `condition`, `policy_rule`, `extra`, and `construction.body`) | JSON text | `condition = jsonencode(yamldecode(file("${path.module}/only-employees.yaml")))` |

- **Lists for multi-valued items.** Write multi-valued items (`mapping`,
  `item`, `authorization`, `objectType`, `attribute`, ...) as lists even with
  one value. A single object is accepted but is read back as a list.
- **Canonical forms** compared as equal:

  | You write | midPoint returns | Compared as |
  |---|---|---|
  | reference without `relation` | `org:default` | `org:default` |
  | `relation = "manager"` | `org:manager` | `org:manager` |
  | `UserType`, `#UserType`, `c:UserType` | `#UserType` | `UserType` |
  | `name = "x"` | `{orig = "x", norm = "x"}` | `"x"` |
  | one value of a multi-valued item | an object | a one-element list |
  | `costCenter = ["a"]` (one-element list of a scalar, any item) | `"a"` | equal |

  QName folding applies to every string value of the form `#` or `c:`
  followed by a capitalized name ending in `Type`, wherever it appears. A
  prefixed free-text string of that shape (`c:SomeType`) would be rewritten
  too. Values with other prefixes, such as non-reference `type` values like
  `xsd:string` or `ri:AccountObjectClass`, and full-URI `@type` values are
  compared verbatim.
- **References** (`target` blocks) carry `oid`, `filter` (a midPoint text
  query such as `name = "vault / operator"`), or both, plus `type` and
  optional `relation`. With a filter only, Terraform knows no dependency on
  the target. Order it with `depends_on` when the target is in the same
  configuration. With a filter and no configured `oid`, `oid` is computed:
  the OID midPoint resolved the filter to, recorded on apply, refresh and
  import (so filter-only HCL plans clean after an import). It is kept while
  `filter`, `type` and `relation` stay the same, is unknown in the plan when
  one of them changes, and is never sent to midPoint. A `relation` without a
  prefix is in the `org:` namespace (`manager` is `org:manager`).
- **`archetype` shortcut** on service, role, org and archetype: an
  assignment holding only the archetype reference. On read it folds back into
  `archetype`. Setting `archetype` and also writing an explicit archetype
  assignment block is a validation error.
- **`extra`** may not hold items that a typed attribute expresses.
  `extra.assignment` and `extra.inducement` are allowed (for values a block
  cannot express) and stay in `extra` once placed there.
- **Files.** Keep large containers in YAML next to the HCL and load them with
  `yamldecode(file("${path.module}/x.yaml"))`. See
  [`examples/resources/midpoint_resource`](examples/resources/midpoint_resource).

## Known limitations

| Area | Limitation |
|---|---|
| JSON text in blocks | Plan-time comparison of the in-block JSON strings is textual. `jsonencode(...)` output plans clean after an import; a hand-written JSON literal with a different key order plans one no-op update. |
| Single-object multi-valued items | A single-object `mapping`/`item` (or other multi-valued item) in config imports with one no-op update. Write lists. |
| Protected strings | A configured `clearValue` never equals the server's `encryptedData`, so it always plans a change. Use `expression.const` or midPoint secret providers. `midpoint_resource.connector_configuration` warns on `clearValue`. `data.midpoint_object` removes protected strings from `body`. |
| Resource `@ns` | `connector_configuration` needs `"@ns"` (the connector namespace) on `configurationProperties`. `"@ns"` is never compared: a change to it alone is not detected. A delta that adds the whole container (adopting or importing a resource without one, or restoring it after it was removed in midPoint) writes the properties without their namespace. Set them in midPoint first in that case. |
| Resource schema definition | An indented `schema.definition` never converges: midPoint adds `xml:space="preserve"` to it. Keep the exported one-line form. |
| Resource import | `connector` reads back null (the resolved connector is `connector_oid`), so a config plans one no-op update after an import. A schema definition midPoint cached is not imported. |
| Task `initial_execution_state` | Applies on create only. A change re-creates a task the provider created; imported or adopted tasks are never replaced for it. |
| Task import | `owner` and `binding` read back as null until configured, so a config that sets them plans one no-op update after an import. |
| `archetype` with raw writes | Service, role and archetype write with `raw` by default, which skips midPoint's recompute. A create writes `archetypeRef` and `roleMembershipRef` with the archetype assignment; changing `archetype` later only changes the assignment, so `archetypeRef` and `roleMembershipRef` keep the old archetype until midPoint recomputes the object. Both are server-managed and never compared. |
| `extension` needs a schema extension | Extension items must be defined by a midPoint schema extension. Without one, midPoint rejects the item on a write that is not raw ("No definition in PP(costCenter)"; the provider reports it and creates nothing). A raw write (the default for service, role and archetype; org writes are not raw) stores it without a definition, and midPoint returns it as `#costCenter`, which never matches the configuration: the plan keeps showing it. Verified on midPoint 4.9 by `TestAccRole_extension`. |
| Delete checks on midPoint 4.9 | midPoint 4.9 never returns `X-Total-Count`, so a refused delete reports "at least N" members or shadows. Members are searched in `assignmentHolders` and shadows in `shadows`, both with `raw` (a `noFetch` shadow search fails on 4.9). |
| Test password | midPoint 4.9's default password policy rejects `5ecr3t`; the acceptance environment uses `Test5ecr3t`. |

## Development

| Command | What it does |
|---|---|
| `make build` | `go build ./...` |
| `make test` | Unit tests (`go test ./...`), no server needed. |
| `make lint` | `gofmt -l .` must be empty, then `go vet ./...`. |
| `make docs` | Regenerates `docs/` with `tfplugindocs` from the schema descriptions, `templates/` and `examples/`. Commit the result; CI fails on a stale `docs/`. |
| `make validate-examples` | Builds the provider into `dist/` and runs `terraform init -backend=false` and `terraform validate` on `examples/complete`, `examples/provider` and every `examples/resources/*` and `examples/data-sources/*` directory, through a dev override. |
| `make env-up` | Starts midPoint and PostgreSQL with Docker Compose (`testacc/`) and waits until REST authenticates. |
| `make testacc` | Acceptance tests (`TF_ACC=1`) against the running server. |
| `make env-down` | Removes the containers and volumes. |

### Acceptance tests

```sh
make env-up                             # midPoint 4.9 at http://localhost:8080
make testacc                            # all TestAcc* tests
make testacc TESTARGS='-run TestAccRole' # a subset
make env-down
```

- Credentials: `administrator` / `Test5ecr3t` (the Makefile exports
  `MIDPOINT_URL=http://localhost:8080`, `MIDPOINT_USERNAME` and
  `MIDPOINT_PASSWORD`; override them to test against another server).
- `make testacc` does not start a server.
- Another midPoint version: `MIDPOINT_VERSION=4.10 make env-up` (image
  `evolveum/midpoint:${MIDPOINT_VERSION}-alpine`, default `4.9`). 4.11 has no
  `4.11-alpine` tag, so use `MIDPOINT_VERSION=4.11.0`. Run `make env-down` first
  so the repository is created fresh.
- `TestAccResource_stateFromV020PlansEmpty` downloads provider 0.2.0 from the
  Terraform Registry.
- More on the environment: [`testacc/README.md`](testacc/README.md).
- Versions tested, with results: [`docs/compatibility.md`](docs/compatibility.md).

### CI

`.github/workflows/ci.yml`:

- `unit`, on every push and pull request: `make lint`, `go test ./...`,
  `make docs` with a check that `docs/` and `examples/` are unchanged, and
  `make validate-examples`.
- `acceptance`, on manual dispatch and nightly: `make env-up`, `make testacc`
  and `make env-down` for `MIDPOINT_VERSION` 4.9, 4.10 and 4.11.0. On failure the
  `docker compose logs` are uploaded as an artifact.

## Examples

- [`examples/complete`](examples/complete): a tenant with a department, a
  service with two roles (archetypes looked up with `data.midpoint_object`), a
  business role with a name-filter inducement, an org with inducements, a
  suspended task, and the provider configured from variables.
- [`examples/resources`](examples/resources) and
  [`examples/data-sources`](examples/data-sources): one example per resource
  and data source, rendered into `docs/`.

## License

[European Union Public Licence v. 1.2 (EUPL-1.2)](LICENSE), from v0.3.0 on.
Releases up to v0.2.0 were released under the Apache License 2.0.

The schema tables are generated from midPoint's XSD files; see
[`internal/mpschema/tables/NOTICE`](internal/mpschema/tables/NOTICE).
