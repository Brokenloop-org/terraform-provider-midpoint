# Acceptance-test environment

A local midPoint (native PostgreSQL repository) for `make testacc`, adapted
from the official Evolveum docker-compose layout.

```sh
make env-up      # docker compose up -d, then block until REST authenticates
make testacc     # TF_ACC=1 go test ./internal/provider/ -run TestAcc
make env-down    # remove containers and volumes (fresh repository next time)
```

`make testacc` needs a running server. It does not start one itself, so you
can point `MIDPOINT_URL` at another server.

- URL: `http://localhost:8080/midpoint` (`MIDPOINT_URL=http://localhost:8080`)
- Credentials: `administrator` / `Test5ecr3t`
- Image: `evolveum/midpoint:${MIDPOINT_VERSION:-4.9}-alpine`, plus `postgres:16-alpine`
- Compose project name: `midpoint-testacc`

`wait.sh` polls `GET /midpoint/ws/rest/self` every 5 s for up to 10 minutes.
On timeout it prints the last 50 lines of the `midpoint_server` log and exits
non-zero.

## Administrator password

midPoint's default password policy needs at least 8 characters and at least
one uppercase letter. With `MP_SET_midpoint_administrator_initialPassword=5ecr3t`,
importing `050-user-administrator.xml` fails ("Minimal size (8) is not met
... Minimal occurrence (1) of characters (Uppercase characters) is not met").
The server still starts, but there is no administrator user and every REST
call returns 401. The environment therefore uses `Test5ecr3t`, which is also
the default in Evolveum's own compose file.

## First start

Measured on 2026-10-22 with the images already pulled (Docker Desktop, macOS):
`make env-up` from empty volumes took 23 s in total. REST answered 16 s after
the containers started, and Tomcat was up about 11 s after the JVM started.
Pulling the images (about 1.2 GB) comes on top of that the first time.

## Normalization fixtures

`internal/normalize/testdata/real/` holds raw REST responses from this
environment (midPoint 4.9), recorded with `Accept: application/json` and no
`options` (no `exclude`, no `raw`). They were only reformatted with `jq .`:

| File | Request |
|------|---------|
| `role-superuser.json` | `GET /ws/rest/roles/00000000-0000-0000-0000-000000000004` |
| `archetype-application.json` | `GET /ws/rest/archetypes/00000000-0000-0000-0000-000000000329` |
| `org-fixture.json` | `GET /ws/rest/orgs/{oid}` after `POST /ws/rest/orgs` with `{"org":{"name":"fixture-org","assignment":{"targetRef":{"oid":"00000000-0000-0000-0000-000000000329","type":"ArchetypeType"}}}}` |
| `task-fixture.json` | `GET /ws/rest/tasks/{oid}` after `POST /ws/rest/tasks` and `POST /ws/rest/tasks/{oid}/suspend` |
| `search-roles-superuser.json` | `POST /ws/rest/roles/search?returnTotalCount=true` (see below) |
| `resource-csv.json` | `GET /ws/rest/resources/{oid}?options=noFetch` after creating the acceptance CSV resource (below) and `POST /ws/rest/resources/{oid}/test` |
| `resource-csv-synchronization.json` | `GET /ws/rest/resources/{oid}?options=noFetch` after creating the resource of `examples/resources/midpoint_resource` (no test) |

The task was created with this body (no `ownerRef` and no `executionState`, so
the fixture shows what the server fills in):

```json
{"task":{"name":"fixture-task","schedulingState":"ready","binding":"loose","activity":{"work":{"recomputation":{"objects":{"type":"UserType","query":{"filter":{"text":"name = \"fixture-nobody\""}}}}}}}}
```

`POST /tasks` answered `202 Accepted`. The task ran once before the suspend
call arrived. Its query matches no users, so it changed nothing. As a result
the fixture includes run-time state (`lastRunStartTimestamp`,
`lastRunFinishTimestamp`, `completionTimestamp`, `operationStats`,
`activityState`, `progress`, `resultStatus`, `stateBeforeSuspend`,
`schedulingStateBeforeSuspend`, `affectedObjects`) next to
`executionState`/`schedulingState: suspended` and a server-set `ownerRef`
(administrator). Both objects were deleted after recording.

Every GET response wraps the object in its singular type key
(`{"role": {...}}`). The first key in the object is `@metadata` (storage and
process timestamps), not `oid`.

To re-record, run `make env-down && make env-up`, repeat the requests above
with `curl -u administrator:Test5ecr3t -H 'Accept: application/json'`, pipe
the output through `jq .`, and delete the org and task afterwards.

The CSV resources were created with `POST /ws/rest/resources?options=isImport&options=reevaluateSearchFilters`,
their YAML parts converted to JSON, and `connectorRef` written as the filter
`connectorType = "com.evolveum.polygon.connector.csv.CsvConnector" and connectorVersion = "2.8"`.
`resource-csv.json` uses `testacc/fixtures/csv-resource/connector-configuration.yaml`
and `schema-handling.yaml` (name `fixture-csv`, description `CSV fixture`);
the connection test made midPoint cache `schema/definition` with
`schema/cachingMetadata`, add `capabilities/native` with
`capabilities/cachingMetadata`, and record `operationalState` and
`operationalStateHistory`. `resource-csv-synchronization.json` adds the
example's capabilities and generation constraints. Both resources were
deleted after recording; the connector OID in them is this environment's.

## Connector resources

`midpoint_server` mounts `testacc/fixtures/data` read-only at
`/opt/midpoint/var/data`; `accounts.csv` there is the data of the CSV
resource the `TestAccResource_*` tests create. Observed on midPoint 4.9 with
the bundled CSV connector 2.8:

- `configurationProperties` need `"@ns"` set to the connector namespace
  (`http://midpoint.evolveum.com/xml/ns/public/connector/icf-1/bundle/com.evolveum.polygon.connector-csv/com.evolveum.polygon.connector.csv.CsvConnector`).
  Without it midPoint stores the properties unqualified (`"#filePath"`) and
  the connection test fails with "No configuration properties found. Wrong
  namespace?". `"@ns"` must be the first key of its JSON object ("Namespace
  declared after other fields").
- On a read-only mount the connector needs `readOnly: true` and a writable
  `tmpFolder` (`/tmp`), or initialization fails with "Can't write to file"
  / "Can't write to tmp folder".
- `schema/definition` must be written as `{"@ns": "http://www.w3.org/2001/XMLSchema", "schema": "<xsd:schema ...>"}`;
  without the `"@ns"` both create and modify fail with "Cannot parse
  schema". A one-line schema round-trips verbatim; an indented one comes back
  trimmed and with `xml:space="preserve"` added to the root element.
- `POST /ws/rest/resources/{oid}/import/AccountObjectClass` answers `303` to
  the import task, which finishes within a second and creates the two
  shadows. Deleting the resource leaves the shadows in the repository.
- A shadow search across a resource works with `options=raw`; with
  `options=noFetch` alone it fails ("Couldn't search objects in
  provisioning").
- Every modification moves a resource's `version` by two (the operation
  execution record is a second repository update).

## Search response shape

`POST /ws/rest/roles/search?returnTotalCount=true` with body
`{"query":{"filter":{"text":"name = \"Superuser\""},"paging":{"maxSize":5}}}`
returned 200 with these headers:

```
HTTP/1.1 200
X-Served-By: DefaultNode
X-Content-Type-Options: nosniff
X-XSS-Protection: 0
Cache-Control: no-cache, no-store, max-age=0, must-revalidate
Pragma: no-cache
Expires: 0
X-Frame-Options: DENY
Content-Type: application/json
Transfer-Encoding: chunked
```

**There is no `X-Total-Count` header.** It is also missing with
`?options=returnTotalCount` and with no options. The body has no count field
either.

The body is an `ObjectListType` wrapped once more under `object`. The result
list is always a JSON array, even when there is only one match:

```json
{
  "@ns": "http://prism.evolveum.com/xml/ns/public/types-3",
  "object": {
    "@ns": "http://midpoint.evolveum.com/xml/ns/public/common/api-types-3",
    "@type": "http://midpoint.evolveum.com/xml/ns/public/common/api-types-3#ObjectListType",
    "object": [
      { "@ns": "http://midpoint.evolveum.com/xml/ns/public/common/common-3",
        "@type": "c:RoleType", "oid": "00000000-0000-0000-0000-000000000004", "name": "Superuser", ... }
    ]
  }
}
```

With no matches, the inner `object` key is missing:
`{"@ns":"...types-3","object":{"@type":"...#ObjectListType"}}`.
