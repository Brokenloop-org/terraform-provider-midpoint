package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
)

// The LDAP connector that ships with the midPoint image, and the namespace
// of its configuration properties.
const (
	accLDAPConnectorType = "com.evolveum.polygon.connector.ldap.LdapConnector"
	accLDAPNamespace     = "http://midpoint.evolveum.com/xml/ns/public/connector/icf-1/bundle/com.evolveum.polygon.connector-ldap/com.evolveum.polygon.connector.ldap.LdapConnector"
)

// accLDAPResourceConfig is a midpoint_resource "ldap" configured from the
// LDAP fixture, with host and the secret's key replaced, and extra
// attribute lines.
func accLDAPResourceConfig(t *testing.T, name, host, key, attrs string) string {
	t.Helper()
	file, err := filepath.Abs(filepath.Join("..", "..", "testacc", "fixtures", "ldap-resource", "connector-configuration.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`
locals {
  ldap = yamldecode(file(%q))
}

resource "midpoint_resource" "ldap" {
  name = %q
  connector {
    type = %q
  }
  connector_configuration = {
    configurationProperties = merge(local.ldap.configurationProperties, {
      host         = %q
      bindPassword = { externalData = { provider = "env", key = %q } }
    })
  }
%s
}
`, file, name, accLDAPConnectorType, host, key, attrs)
}

// accLDAPStored checks the configuration properties midPoint stores for
// addr: host and the secret's reference, read back without raw, and with
// raw that they are in the connector's namespace.
func accLDAPStored(c *client.Client, addr, host, key string) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		oid, err := accResourceOID(st, addr)
		if err != nil {
			return err
		}
		body, err := accGetResource(c, oid)
		if err != nil || body == nil {
			return fmt.Errorf("reading the resource: %v (found %v)", err, body != nil)
		}
		cc, _ := body["connectorConfiguration"].(map[string]any)
		cp, _ := cc["configurationProperties"].(map[string]any)
		if cp["host"] != host {
			return fmt.Errorf("stored host = %v, want %s", cp["host"], host)
		}
		want := map[string]any{"externalData": map[string]any{"provider": "env", "key": key}}
		if !reflect.DeepEqual(cp["bindPassword"], want) {
			return fmt.Errorf("stored bindPassword = %#v, want %#v", cp["bindPassword"], want)
		}
		raw, err := c.Get(context.Background(), "resources", oid, client.GetOptions{Options: []string{"raw", "noFetch"}})
		if err != nil {
			return err
		}
		rcc, _ := raw.Body["connectorConfiguration"].(map[string]any)
		rcp, _ := rcc["configurationProperties"].(map[string]any)
		if rcp["@ns"] != accLDAPNamespace {
			return fmt.Errorf("configuration properties stored in namespace %v, want %s", rcp["@ns"], accLDAPNamespace)
		}
		return nil
	}
}

// TestAccResource_connectorSecret: a protected connector property given as
// an externalData reference is created, kept with an empty plan, and
// changed in place together with another property, without raw. Only the
// reference is in state.
func TestAccResource_connectorSecret(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-ldap")
	const addr = "midpoint_resource.ldap"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accResourceGone(c),
		Steps: []resource.TestStep{
			{
				Config: accLDAPResourceConfig(t, name, "ldap.invalid", "MP_TESTACC_LDAP_SECRET", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr,
						tfjsonpath.New("connector_configuration").AtMapKey("configurationProperties").AtMapKey("bindPassword"),
						knownvalue.ObjectExact(map[string]knownvalue.Check{"externalData": knownvalue.ObjectExact(map[string]knownvalue.Check{
							"provider": knownvalue.StringExact("env"), "key": knownvalue.StringExact("MP_TESTACC_LDAP_SECRET"),
						})})),
				},
				Check: accLDAPStored(c, addr, "ldap.invalid", "MP_TESTACC_LDAP_SECRET"),
			},
			emptyPlanAfterRefresh(),
			{
				Config: accLDAPResourceConfig(t, name, "ldap2.invalid", "MP_TESTACC_LDAP_SECRET_2", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: accLDAPStored(c, addr, "ldap2.invalid", "MP_TESTACC_LDAP_SECRET_2"),
			},
			emptyPlanAfterRefresh(),
		},
	})
}

// TestAccResource_adoptWithoutConnectorConfiguration: adopting a resource
// that has no connector configuration adds it whole, and midPoint stores the
// properties in the connector's namespace (the configured "@ns" is sent).
func TestAccResource_adoptWithoutConnectorConfiguration(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-ldap-adopt")
	const addr = "midpoint_resource.ldap"
	oid := uuid.NewString()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accResourceGone(c),
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					_, err := c.Create(context.Background(), client.Object{Type: "resources", Body: map[string]any{
						"oid":          oid,
						"name":         name,
						"connectorRef": connectorRefValue(accLDAPConnectorType, ""),
					}}, []string{"reevaluateSearchFilters"})
					if err != nil {
						t.Fatalf("creating the resource to adopt: %v", err)
					}
				},
				Config: accLDAPResourceConfig(t, name, "ldap.invalid", "MP_TESTACC_LDAP_SECRET",
					fmt.Sprintf("  adopt_existing = true\n  force_destroy  = true\n  oid            = %q\n", oid)),
				Check: accLDAPStored(c, addr, "ldap.invalid", "MP_TESTACC_LDAP_SECRET"),
			},
			emptyPlanAfterRefresh(),
		},
	})
}
