---
page_title: "Migrating a JSON catalogue with midpoint-convert"
subcategory: ""
description: |-
  Convert an existing midPoint JSON object catalogue to Terraform configuration and import it without changing midPoint.
---

# Migrating a JSON catalogue with midpoint-convert

## Overview

`midpoint-convert` reads a directory tree of midPoint object files (JSON, YAML
or XML) and writes Terraform configuration for this provider: one `.tf` file
per object kind, one resource per object, and `imports.tf` with an `import`
block per object. Every OID is kept. It reads files only and never contacts a
server.

Inputs from several directories are merged by OID. Identical content is
deduplicated; two files that define the same OID differently stop the run
(exit code 2). The result is one flat set of files that every environment
applies.

The run also writes `report.md`. Read its **Manual follow-up** section first.

## Prerequisites

Build the provider and the converter from the repository root:

```shell
make build
```

This produces `dist/terraform-provider-midpoint` and `dist/midpoint-convert`.

Until the provider is installed from a registry, point Terraform at the local
build with a development override. Write a CLI configuration file and export
it:

```shell
cat > dist/terraformrc <<RC
provider_installation {
  dev_overrides {
    "jplana/midpoint" = "$PWD/dist"
  }
  direct {}
}
RC
export TF_CLI_CONFIG_FILE="$PWD/dist/terraformrc"
```

With a dev override, skip `terraform init` for the provider; Terraform warns
about the override and uses the binary in `dist/`. The converted configuration
needs a `required_providers` entry for `jplana/midpoint` and a `provider
"midpoint"` block with the server URL and credentials (see the provider
documentation).

## Step 1: Convert

Pass the base catalogue directory and any per-environment directories, which
should only add to it or duplicate it:

```shell
dist/midpoint-convert --out converted catalogue/ overlays/staging/
```

`--out` must not exist or must be empty; the converter refuses anything else
and never overwrites. The directory is written as a whole, so a failed run
leaves it as it was.

| Flag | Meaning |
|---|---|
| `--format hcl\|json` | `hcl` (default) or Terraform JSON (`.tf.json`). |
| `--split-files` | Move large free-form attributes of `midpoint_resource` (`capabilities`, `connector_configuration`, `extra`, `schema_handling`, `synchronization`, over 20 lines) to `<label>/<attribute>.yaml` files next to the HCL, referenced with `yamldecode(file("${path.module}/..."))`, and the schema definition to `<label>/schema.xsd`. An attribute that contains a Terraform reference stays inline, with a comment. |

## Step 2: Review the conversion report

Open `converted/report.md` and work through it as described in
[The conversion report](#the-conversion-report). Decide for each manual
follow-up item and each `extra` entry whether to fix the HCL or keep it.

## Step 3: Plan the imports against pre-production

The import blocks need Terraform 1.5 or later. Point Terraform at the
`converted` directory, configure the provider for pre-production, then:

```shell
cd converted
terraform plan
```

The plan must show only imports, with no other change. Any other diff is a
provider bug or a stale file in the catalogue; fix it before continuing. See
[Known limitations](#known-limitations) for diffs that are expected.

## Step 4: Apply the imports, then remove them

```shell
terraform apply
```

Delete `imports.tf` (the import blocks have done their work) and plan again.
The plan must be empty:

```shell
rm imports.tf
terraform plan
```

## Step 5: Repeat per environment

Apply the same configuration to every other environment the same way. Then
retire the tooling that managed these objects before.

## The conversion report

`report.md` has these sections, in this order:

- **Summary**: counts of files read, objects converted, files skipped, data
  lookups and manual follow-up items.
- **Manual follow-up**: items that need a human decision before
  `terraform apply`. Read this first.
- **Skipped files**: input files that were not converted, with the reason.
- **Data lookups**: built-in objects (OID `00000000-0000-0000-0000-*`)
  referenced but not in the input, emitted as `data.midpoint_object` blocks.
- **Converted objects**: one row per source file with its resource address.
- **Notes by file**: per file, what was dropped as server-managed, what was
  placed in `extra`, how references were rewritten, and other remarks.
- **Other notes**: remarks that match no converted object.

## What the converter changes and why

### References

A reference to an object that is in the input becomes a Terraform reference,
so the dependency graph replaces `depends_on` chains. A reference with a name
filter keeps the filter as well, so midPoint still sees the same query:

```hcl
inducement {
  target {
    oid    = midpoint_role.grafana_viewer.oid
    type   = "RoleType"
    filter = "name = \"grafana / viewer\""
  }
}
```

A reference to a built-in object that is not in the input (OID
`00000000-0000-0000-0000-*`, an object you do not customize) becomes a data
lookup by OID:

```hcl
data "midpoint_object" "user_2" {
  type = "users"
  oid  = "00000000-0000-0000-0000-000000000002"
}
```

A reference from an object to itself stays a literal OID. So does a reference
to any other OID that is neither in the input nor built-in; the report lists
it under "Notes by file". For each, either add the object's file to the input
and convert again, or replace the literal with a `data.midpoint_object` lookup
by hand.

Objects that reference each other in a cycle (A to B to A, or longer, such as
an archetype whose object template refers back to the archetype) cannot be
ordered by Terraform. The converter breaks each cycle: one reference of the
cycle (the one from the lexically largest `source → target` pair) stays a
literal OID, and the report notes it on the source object, naming both ends
and the whole cycle. In this guide's flow the objects already exist, so the
literal OID changes nothing. On a server where they do not exist yet,
Terraform does not wait for the target of that one reference.

### Filters are text

Structured filters stored in midPoint, built of `equal` conditions joined by
`and`, `or` and `not`, are read back as their text (Axiom) rendering, so the
HCL carries text filters such as `name = "grafana / viewer"`. The operands of
`and` and `or` are sorted in the rendering, so their stored order does not
matter.

When the rendering of a stored structured filter equals the text filter in
the configuration, the provider treats the two as equal: the import plans
clean, and an apply that changes another attribute of the object (its
description, say) sends only that change. The stored filter stays in its
structured form and is never rewritten. Changing the `filter` value in the
HCL is a change the plan shows, and the apply writes the new text filter.

A stored filter that has no text rendering (for example a `substring` or a
`matching` rule) is not converted to a `filter` attribute: the whole
assignment or inducement holding it is placed in `extra` and listed in the
report.

### Built-in objects

Objects with an OID of the form `00000000-0000-0000-0000-*` already exist on
every server. The converter writes them with `adopt_existing = true`, and an
import records the same value, so the imported object plans clean:

```hcl
resource "midpoint_archetype" "application_role" {
  name           = "Application role"
  oid            = "00000000-0000-0000-0000-000000000328"
  adopt_existing = true
}
```

In this guide's flow built-ins are imported, so no adoption warning appears.
If you apply the HCL to a fresh server without the import blocks, the create
adopts the existing object: it takes the whole configuration, items the HCL
does not configure are removed, and the plan warning lists them. Deleting an
adopted object requires `force_destroy = true`.

### Imports

`imports.tf` holds one block per object. The plan shows them as imports:

```hcl
import {
  to = midpoint_archetype.application_role
  id = "00000000-0000-0000-0000-000000000328"
}
```

### Resource connectors

An imported `midpoint_resource` gets its `connector` (`type`, `version`) from
a `connectorRef` that filters on connector type and version, as a text filter
or a structured one with the conditions in either order. Like any other
filter, a structured `connectorRef` filter that renders to the configured
connector is never rewritten. A `connectorRef`
that holds only an OID cannot be converted, because `connector_oid` is
computed and cannot be configured; the report lists it under manual follow-up
and you set `connector { type, version }` by hand.

### Task owner and binding

When `owner` or `binding` is unset in the configuration of an imported task,
the value midPoint holds stays in state, follows the server, and is neither
sent nor compared. Set either attribute in the HCL to manage it. A task
`ownerRef` that is not a plain user reference by OID is dropped and listed
under manual follow-up.

### Items without a typed attribute

Items no typed attribute covers go to `extra` (or `body` for
`midpoint_object`) and are listed per file in the report under "placed in
extra". Server-managed items in the input are dropped and listed under
"dropped as server-managed".

## Known limitations

- **Reference cycles.** One reference per cycle is left as a literal OID (see
  [References](#references)), so it creates no Terraform dependency.
- **OID-only `connectorRef`.** See [Resource connectors](#resource-connectors).
- **Strings in another Unicode form.** The provider compares strictly. If
  midPoint returns a string in a different Unicode normal form than the file
  holds (for example decomposed accents), the plan shows that difference on
  every run. Change the HCL to the form the server returns.
- **XML and YAML inputs.** XML carries no types, so every leaf element is read
  as a string, and the HCL carries strings where JSON would have numbers or
  booleans. Review the plan for such differences.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Done. |
| 1 | Usage or I/O error, including an `--out` directory that is not empty. |
| 2 | Two files define the same OID with different content. |
