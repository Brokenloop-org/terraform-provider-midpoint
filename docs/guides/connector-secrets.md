---
page_title: "Connector secrets"
subcategory: ""
description: |-
  Give a connector password as a reference to a midPoint secrets provider, so that the secret stays out of Terraform state.
---

# Connector secrets

A connector password, or any other protected connector property, can be given
to `midpoint_resource` in two forms:

- **A reference to a midPoint secrets provider** (`externalData`). midPoint
  reads the secret when it uses the connector. Terraform state holds only the
  reference: the provider's identifier and the key. This is the form to use.
- **A clear value** (`clearValue`). The secret is stored in Terraform state in
  plain text. midPoint stores it encrypted, so it never compares equal and every
  plan sends it again. The provider warns about it.

## Register a secrets provider in midPoint

midPoint resolves `externalData` through a secrets provider declared in the
system configuration. This one reads environment variables of the midPoint
process and is called `env`:

```yaml
secretsProviders:
  environmentVariables:
    - identifier: env
```

midPoint's documentation on secrets providers describes the other kinds and
their settings. The provider does not set this up: the system configuration is usually managed
outside the resources that use it.

The secret must reach the midPoint process. On Kubernetes, with the variable
taken from a Secret through `envFrom`, a changed Secret is seen only after the
pod restarts.

## Reference the secret

```terraform
resource "midpoint_resource" "ldap" {
  name = "Corporate LDAP"
  connector {
    type = "com.evolveum.polygon.connector.ldap.LdapConnector"
  }
  connector_configuration = {
    configurationProperties = {
      "@ns"        = "http://midpoint.evolveum.com/xml/ns/public/connector/icf-1/bundle/com.evolveum.polygon.connector-ldap/com.evolveum.polygon.connector.ldap.LdapConnector"
      host         = "ldap.example.com"
      port         = 636
      bindDn       = "cn=midpoint,ou=services,dc=example,dc=com"
      bindPassword = { externalData = { provider = "env", key = "LDAP_BIND_PASSWORD" } }
    }
  }
}
```

The same in a YAML file read with `yamldecode(file("connector.yaml"))`:

```yaml
configurationProperties:
  "@ns": http://midpoint.evolveum.com/xml/ns/public/connector/icf-1/bundle/com.evolveum.polygon.connector-ldap/com.evolveum.polygon.connector.ldap.LdapConnector
  host: ldap.example.com
  bindPassword:
    externalData:
      provider: env
      key: LDAP_BIND_PASSWORD
```

midPoint returns the reference as it was written, so the plan stays empty.
Changing the key, or switching from a clear value to a reference, is an
in-place update.

## Check that midPoint finds the secret

midPoint resolves the reference only when it uses the connector, not when the
resource is written. Test the resource's connection in midPoint: a missing
variable is reported as `No secret with key LDAP_BIND_PASSWORD found in provider
env`.

This was verified on midPoint 4.11.0 by `TestAccResource_connectorSecret`, and
by hand with the `env` provider for the connection test.
