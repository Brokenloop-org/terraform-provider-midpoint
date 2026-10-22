# midPoint compatibility

The acceptance suite (`make testacc`, every `TestAcc*` test in
`internal/provider`) run against the Docker environment in `testacc/`, one
midPoint version at a time:

```sh
make env-down
MIDPOINT_VERSION=4.10 make env-up   # omit MIDPOINT_VERSION for 4.9
make testacc
```

`make env-down` first, so every run starts from a fresh repository.

| midPoint | Image tag | Date | Result | Notes |
|---|---|---|---|---|
| 4.10 | `evolveum/midpoint:4.10-alpine` | 2026-10-22 | pass, 19/19 | First run 15/19: four acceptance tests assumed 4.9; the tests were adjusted (see below). No normalization rule, client or engine change was needed. |
| 4.9 | `evolveum/midpoint:4.9-alpine` | 2026-10-22 | pass, 19/19 | Rerun after the 4.10 changes; no regression. |

Server-reported versions (`build/version` and `build/revision` of
`GET /ws/rest/nodes/current`, which the provider logs at Configure as
`connected to midPoint ... version=`):

| Image tag | `build/version` | `build/revision` | Image ID |
|---|---|---|---|
| `evolveum/midpoint:4.10-alpine` | `4.10` | `v4.10` | `sha256:b549b8e8cfaa…` |
| `evolveum/midpoint:4.9-alpine` | `4.9` | `v4.9` | `sha256:05ffde259a1e…` |

The database was `postgres:16-alpine` in both runs. The environment came up
unchanged on 4.10: `MP_SET_midpoint_administrator_initialPassword` still sets
the administrator password and `testacc/wait.sh` authenticated within 15 s.

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
