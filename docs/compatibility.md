# midPoint compatibility

The acceptance suite (`make testacc`, every `TestAcc*` test in
`internal/provider`) run against the Docker environment in `testacc/`, one
midPoint version at a time:

```sh
make env-down
MIDPOINT_VERSION=4.10 make env-up   # omit MIDPOINT_VERSION for 4.9
make testacc
```

`make env-down` first, so every run starts from a fresh repository. The image tag
is `${MIDPOINT_VERSION}-alpine`. There is no `4.11-alpine` tag, so use
`MIDPOINT_VERSION=4.11.0`.

| midPoint | Image tag | Date | Result | Notes |
|---|---|---|---|---|
| 4.11.0 | `evolveum/midpoint:4.11.0-alpine` | 2026-11-06 | pass, 32/32 | First run 27/32: two existing tests assumed older versions and were adjusted, the new mistyped-key test recorded a difference, and `cacheInvalidationTimestamp` needed a provider change. See below. |
| 4.10.5 | `evolveum/midpoint:4.10.5-alpine` | 2026-11-06 | pass, 32/32 | |
| 4.10 | `evolveum/midpoint:4.10-alpine` | 2026-11-06 | pass, 32/32 | |
| 4.9.9 | `evolveum/midpoint:4.9.9-alpine` | 2026-11-06 | pass, 32/32 | |
| 4.9 | `evolveum/midpoint:4.9-alpine` | 2026-11-06 | pass, 32/32 | |
| 4.10 | `evolveum/midpoint:4.10-alpine` | 2026-10-22 | pass, 19/19 | First run 15/19: four acceptance tests assumed 4.9; the tests were adjusted (see below). No normalization rule, client or engine change was needed. |
| 4.9 | `evolveum/midpoint:4.9-alpine` | 2026-10-22 | pass, 19/19 | Rerun after the 4.10 changes; no regression. |

Server-reported versions (`build/version` and `build/revision` of
`GET /ws/rest/nodes/current`, which the provider logs at Configure as
`connected to midPoint ... version=`):

| Image tag | `build/version` | `build/revision` | Image ID |
|---|---|---|---|
| `evolveum/midpoint:4.11.0-alpine` | `4.11.0` | `v4.11.0` | `sha256:fe29be6d4c8d…` |
| `evolveum/midpoint:4.10.5-alpine` | `4.10.5` | `v4.10.5` | `sha256:b34ec6183ea8…` |
| `evolveum/midpoint:4.10-alpine` | `4.10` | `v4.10` | `sha256:b549b8e8cfaa…` |
| `evolveum/midpoint:4.9.9-alpine` | `4.9.9` | `v4.9.9` | `sha256:27c0f7e22b6a…` |
| `evolveum/midpoint:4.9-alpine` | `4.9` | `v4.9` | `sha256:05ffde259a1e…` |

The database was `postgres:16-alpine` in every run. The environment came up
unchanged on 4.10: `MP_SET_midpoint_administrator_initialPassword` still sets
the administrator password and `testacc/wait.sh` authenticated within 15 s.

## 4.11 differences found

### Fixed: `cacheInvalidationTimestamp` showed as drift

After a connection test or an import task, midPoint 4.11 sets
`cacheInvalidationTimestamp` on the resource. Per its schema, "if specified, all
shadows cached on or before this time are considered expired". The provider used
to plan removing it, and an apply deleted it from midPoint.

It is now compared only when the configuration sets it:

- A value midPoint stamped or someone set by hand is not drift when the
  configuration does not set the item. An apply leaves it in place.
- A configured value is compared and written. On 4.11, a resource that configures
  this item shows a write-back in the plan after each connection test, because the
  test stamps a new time, and applying that plan writes the configured time back.

### Test assumptions, adjusted

The provider's behaviour was right in both cases.

1. **CSV connector name.** The name is now `ConnId CSV Connector v2.10`, without
   `CsvConnector`. `TestAccDataConnector_csv` matches it in either form.
2. **An undefined extension item fails differently.** A non-raw write is still
   refused and creates nothing, but with HTTP 500, "An attempt to modify an
   immutable", from an `IllegalStateException` in midPoint's projector.
   `TestAccRole_extension` accepts this message as well as the older one.

### Other differences

A mistyped key on a non-raw update fails with the same HTTP 500, where older lines
lose the key. `TestAccValueForms_mistypedExtraKey` records this. See
[Mistyped keys at apply](#mistyped-keys-at-apply).

## 4.10 differences found

Each difference was a test assumption tied to 4.9. The provider already
handled the server behaviour.

1. **Bundled CSV connector 2.9 (was 2.8).** `TestAccResource_csv`,
   `TestAccResource_forceDestroyWithShadows` and `TestAccResource_example`
   pinned version 2.8, which the 4.10 image does not ship ("Repository
   reference connectorRef cannot be resolved: filter matches no object").
   The tests now look up the version of the one CSV connector the
   environment has, and still configure the connector by type and version.
   `examples/resources/midpoint_resource` keeps 2.8 (the 4.9 connector). On
   4.10, set `connector.version` to 2.9, take it from the
   `midpoint_connector` data source, or leave it unset while only one CSV
   connector version is installed.
2. **Searches return `X-Total-Count`.** midPoint 4.10 answers
   `returnTotalCount=true` with an `X-Total-Count` header, which 4.9 omits.
   The client already read it, so a refused delete now says
   `1 objects still reference it` where 4.9 gave `at least 1 objects ...`.
   The shorter message wraps at a different word in Terraform's output, and
   `TestAccRole_forceDestroyRequiredWithMember` matched `still reference it`
   with literal spaces. Its pattern, and the one in
   `TestAccResource_forceDestroyWithShadows`, now allow any whitespace
   between the words.
3. **Successful operations are not recorded.** The initial system
   configuration of a 4.10 repository sets
   `internals/operationExecutionRecording/skipWhenSuccess = true`, so a
   successful modify writes no `operationExecution` record. Under 4.9 every
   modify writes one, which moves a resource's `version` by two.
   `TestAccResource_csv` counts the records to prove that a one-mapping
   change is exactly one modify. The test now turns `skipWhenSuccess` off
   for its own duration and restores it at cleanup. The check is unchanged.
   A repository upgraded from 4.9 keeps its existing system configuration,
   so it keeps recording.

No new server-managed item showed up on 4.10: every
`emptyPlanAfterRefresh` and import-verify step passed with the 4.9
normalization rules, including the resource after a connection test (cached
schema, native capabilities, operational state).

## Invalid item checks

At plan time the provider checks free-form attributes against the schema of the
server's midPoint line. A problem fails the plan, or warns with
`on_invalid_item = "warning"`.

### Schema tables

One table per line, chosen by the major.minor of the version the server reports,
so a server on 4.9.3 gets the 4.9 table. A server on any other line, or one whose
version cannot be read, is not checked, and the provider warns once.

| Line | Source image, pinned by digest in `internal/mpschema/images.txt` |
|---|---|
| 4.9 | `evolveum/midpoint:4.9.9-alpine` |
| 4.10 | `evolveum/midpoint:4.10.5-alpine` |
| 4.11 | `evolveum/midpoint:4.11.0-alpine` |

Each table comes from the newest patch of its line. A key added in a later patch
than the table's is flagged until the table is regenerated, and
`on_invalid_item = "warning"` is the way out until then.

### Adding a line

1. Add `<line> <image@digest>` to `internal/mpschema/images.txt`, using the newest
   patch of the line and its `-alpine` image.
2. Run `make schema-gen`, which writes `internal/mpschema/tables/<line>.json.gz`.
3. Add the line to `tables` in `internal/mpschema/embed.go` and to
   `internal/mpschema/tables/NOTICE`, with the licence of that midPoint version.
4. Run `make test`. The false-alarm corpus runs on every table.
5. Add the line to the matrix in `.github/workflows/ci.yml`, run the acceptance
   suite on it, and record the result in this file.

### What is checked

| Resource | Attributes |
|---|---|
| `midpoint_service`, `midpoint_role`, `midpoint_org`, `midpoint_archetype` | `extra`, `authorization` and `archetype_policy` where the resource has them, and `condition`, `policy_rule`, `extra` and `construction.body` in `assignment` and `inducement` blocks |
| `midpoint_resource` | `schema_handling`, `synchronization`, `capabilities`, `schema.generation_constraints`, `extra` |
| `midpoint_task` | `schedule`, `activity`, `extra` |
| `midpoint_object_template` | `mapping`, `item`, `iteration_specification`, `extra` |
| `midpoint_mark` | `display`, `event`, `extra` |
| `midpoint_object` | `body`, when `type` names a known type |

The whole tree is checked, for:

- **Unknown keys**, including a `#` key outside `extension`, which marks an item
  stored without a definition. This covers items midPoint marks as removed or
  moved: they are not in its schema.
- **Enumerations:** the listed value or its Java constant name (`dryRun` or
  `DRY_RUN`).
- **Booleans:** the plan passes `true`, `false`, `True`, `False`, `TRUE`, `FALSE`,
  `1` and `0`. midPoint 4.9.9 refuses `1` and `0` as strings at apply.
- **Integers:** a whole number, or a string holding one.
- **An object where a plain value belongs.**
- **Several values on a single-valued item.**

Unknown values are skipped and checked on a later plan.

### What is left unchecked

- `extension` and `connector_configuration`: their content is defined per
  deployment or per connector.
- Keys inside references such as `targetRef`.
- Types with an `xsd:any`, types missing from the table, filters (`SearchFilterType`)
  and raw values (`RawType`): the check stops for that subtree.
- Formats such as dates, QNames and item paths, and a plain value given for a
  complex type.
- The `body` of the `midpoint_object` data source, which is output.

### What midPoint does with each value form

Observed by `TestAccValueForms`, the same on full and raw writes and on all five
images in the results table.

| Form | Example | midPoint | Plan |
|---|---|---|---|
| Enumeration, constant name | `STRONG`, `DRY_RUN` | Stored as `strong`, `dryRun` | Passes |
| Enumeration, other case | `Conservative` | HTTP 400 | Flagged |
| Boolean, capitalized | `"TRUE"` | Stored as `true` | Passes |
| Boolean, other | `"yes"` | HTTP 400 | Flagged |
| Integer | `"first"` | HTTP 400 | Flagged |
| Object for a plain value | `riskLevel = { x = "y" }` | **Takes it and stores nothing** | Flagged |
| Several values on a single-valued item | `riskLevel = ["a", "b"]` | HTTP 400 | Flagged |
| Fractional number | `displayOrder = 3.5` | Stored as `3` (seen by hand, on 4.9.9 only) | Flagged |

A value midPoint stores in another form, like `STRONG`, keeps showing as a change
in every plan, so write the listed form. An object for a plain value and a
fractional number stay flagged although midPoint does not refuse them, because what
it keeps is not what was configured.

### Mistyped keys at apply

What a typo in `extra` does with `on_invalid_item = "warning"`, observed by
`TestAccValueForms_mistypedExtraKey`:

| Write | 4.9, 4.10 | 4.11 |
|---|---|---|
| Create | HTTP 400, "Item descripton has no definition"; nothing is created | The same |
| Update, raw | The apply succeeds and the key is lost; every later plan shows it | The same |
| Update, non-raw | The apply succeeds and the key is lost; every later plan shows it | HTTP 500, "An attempt to modify an immutable" |
