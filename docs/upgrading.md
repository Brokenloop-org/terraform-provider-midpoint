# Upgrading

## To v0.3.0

v0.3.0 breaks configuration in two ways. Read this before you upgrade.

A `~> 0.2` version constraint allows 0.3, so the next `terraform init -upgrade`
can bring in these changes. To stay on 0.2 until you are ready, use `~> 0.2.0`.

### `connector` and `schema` are blocks

Every `midpoint_resource` configuration has to change once. Remove the `=`:

```hcl
# Up to v0.2.0
resource "midpoint_resource" "csv" {
  name = "HR"
  connector = {
    type    = "com.evolveum.polygon.connector.csv.CsvConnector"
    version = "2.8"
  }
  schema = {
    generation_constraints = { generateObjectClass = ["ri:AccountObjectClass"] }
  }
}

# From v0.3.0
resource "midpoint_resource" "csv" {
  name = "HR"
  connector {
    type    = "com.evolveum.polygon.connector.csv.CsvConnector"
    version = "2.8"
  }
  schema {
    generation_constraints = { generateObjectClass = ["ri:AccountObjectClass"] }
  }
}
```

If the value comes from a variable, as in `connector = var.connector`, spell
out the attributes it holds. If it can be null, as in `schema = var.schema`, use
a `dynamic` block, which writes nothing when the list is empty:

```hcl
resource "midpoint_resource" "csv" {
  name = "HR"
  connector {
    type    = var.connector.type
    version = var.connector.version
  }
  dynamic "schema" {
    for_each = var.schema == null ? [] : [var.schema]
    content {
      generation_constraints = schema.value.generation_constraints
    }
  }
}
```

The old form fails with a Terraform error. An unknown key in these blocks now
fails `terraform validate`; before, Terraform dropped it without a word. Existing
state carries over: with the block form, a resource applied by v0.2.0 plans no
changes.

### Invalid items fail the plan

The provider now checks free-form attributes such as `extra`, `schema_handling`
and `body` against the schema of the server's midPoint version. An unknown key, or
a value that cannot be right, fails `terraform plan`. A configuration that used to
plan can fail after the upgrade, because of settings that were doing nothing all
along. For example:

```
Error: Invalid midPoint item

extra: descripton is not an item of RoleType in midPoint 4.9. Fix it, or set
on_invalid_item = "warning" if this midPoint version accepts it.
```

Fix the item. If the check is wrong for your server, or you need time to clean
up, set the way out in the provider block:

```hcl
provider "midpoint" {
  on_invalid_item = "warning"
}
```

Two things to know:

- **Items midPoint itself marks as removed or moved now fail the plan.** midPoint
  can still read some of them and only warn. They are not in its schema, so the
  provider treats them as unknown keys. Move them to their new place, or use
  `on_invalid_item = "warning"` until you have.
- **Keys inside references such as `targetRef` are not checked.** A typo there
  still passes the plan and is left to midPoint.

[What is checked and what is not](compatibility.md#invalid-item-checks).

### midPoint 4.11

If you run midPoint 4.11, see
[4.11 differences found](compatibility.md#411-differences-found).

### Licence

Releases up to v0.2.0 were released under the Apache License 2.0. From v0.3.0 on,
the provider is under the European Union Public Licence 1.2 (EUPL-1.2).
