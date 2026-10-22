package provider

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	stubResourceOID  = "7e500000-0000-4000-8000-0000000000c1"
	stubConnectorOID = "c0000000-0000-4000-8000-0000000000c1"
	csvConnectorType = "com.evolveum.polygon.connector.csv.CsvConnector"
	xsdNamespace     = "http://www.w3.org/2001/XMLSchema"
)

// resourceConfig is a midpoint_resource "csv" with the given attributes,
// schema_handling first.
func resourceConfig(schemaHandling, attrs string) string {
	return fmt.Sprintf(`
resource "midpoint_resource" "csv" {
  oid  = %q
  name = "csv"
  connector = {
    type    = %q
    version = "2.8"
  }
  connector_configuration = {
    configurationProperties = {
      "@ns"    = "urn:csv"
      filePath = "/data/accounts.csv"
    }
  }
  schema_handling = %s
%s
}
`, stubResourceOID, csvConnectorType, schemaHandling, attrs)
}

// loginOnly is a schema_handling with one account type and one attribute.
const loginOnly = `{
    objectType = [{
      kind        = "account"
      intent      = "default"
      objectClass = "ri:AccountObjectClass"
      attribute = [
        { ref = "ri:login", inbound = [{ target = { path = "name" } }] },
      ]
    }]
  }`

// loginAndFullName is loginOnly plus a ri:fullName attribute.
const loginAndFullName = `{
    objectType = [{
      kind        = "account"
      intent      = "default"
      objectClass = "ri:AccountObjectClass"
      attribute = [
        { ref = "ri:login", inbound = [{ target = { path = "name" } }] },
        { ref = "ri:fullName", inbound = [{ target = { path = "fullName" } }] },
      ]
    }]
  }`

func TestResource_connectorRefFilter(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_resource.csv"
	cfg := resourceConfig(loginOnly, `
  capabilities = { create = { enabled = false } }
  schema = {
    generation_constraints = { generateObjectClass = ["ri:AccountObjectClass"] }
    definition             = <<-EOT
      <xsd:schema xmlns:xsd="http://www.w3.org/2001/XMLSchema"/>
    EOT
  }
`) + `
resource "midpoint_resource" "any_version" {
  name      = "any version"
  connector = { type = "com.evolveum.polygon.connector.ldap.LdapConnector" }
}
`
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("connector_oid"), knownvalue.Null()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					checkStub(func() error {
						posts := requests(s, "POST")
						if len(posts) != 2 {
							return fmt.Errorf("want two POSTs, got %d", len(posts))
						}
						var csv, ldap map[string]any
						for _, p := range posts {
							if p.Path != "/resources" {
								return fmt.Errorf("POST to %s", p.Path)
							}
							if got, want := p.Query["options"], []string{"isImport", "reevaluateSearchFilters"}; !reflect.DeepEqual(got, want) {
								return fmt.Errorf("create options = %v, want %v", got, want)
							}
							body := asJSON(t, p.Body).(map[string]any)["resource"].(map[string]any)
							if body["name"] == "csv" {
								csv = body
							} else {
								ldap = body
							}
						}
						want := asJSON(t, map[string]any{
							"oid":  stubResourceOID,
							"name": "csv",
							"connectorRef": map[string]any{"type": "ConnectorType", "filter": map[string]any{
								"text": `connectorType = "` + csvConnectorType + `" and connectorVersion = "2.8"`,
							}},
							"connectorConfiguration": map[string]any{"configurationProperties": map[string]any{
								"@ns": "urn:csv", "filePath": "/data/accounts.csv",
							}},
							"schemaHandling": map[string]any{"objectType": []any{map[string]any{
								"kind": "account", "intent": "default", "objectClass": "ri:AccountObjectClass",
								"attribute": []any{map[string]any{"ref": "ri:login", "inbound": []any{
									map[string]any{"target": map[string]any{"path": "name"}},
								}}},
							}}},
							"capabilities": map[string]any{"configured": map[string]any{"create": map[string]any{"enabled": false}}},
							"schema": map[string]any{
								"generationConstraints": map[string]any{"generateObjectClass": []any{"ri:AccountObjectClass"}},
								"definition": map[string]any{
									"@ns":    xsdNamespace,
									"schema": `<xsd:schema xmlns:xsd="http://www.w3.org/2001/XMLSchema"/>`,
								},
							},
						})
						if !reflect.DeepEqual(csv, want) {
							return fmt.Errorf("POST body = %v, want %v", csv, want)
						}
						wantRef := asJSON(t, map[string]any{"type": "ConnectorType", "filter": map[string]any{
							"text": `connectorType = "com.evolveum.polygon.connector.ldap.LdapConnector"`,
						}})
						if !reflect.DeepEqual(ldap["connectorRef"], wantRef) {
							return fmt.Errorf("connectorRef without version = %v, want %v", ldap["connectorRef"], wantRef)
						}
						return nil
					}),
				),
			},
			refreshIsClean(),
			// An import reads back no connector (connector_oid holds the
			// reference) and no schema definition (compared only when
			// configured: it is usually the schema midPoint cached), and
			// the single objectClass in canonical (list) form.
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"connector", "schema.definition", "schema_handling.objectType.0.objectClass",
				},
			},
		},
		CheckDestroy: checkGone(s, "resources", &oid),
	})
}

// midPoint resolves the connector filter to an OID that differs per
// environment: connector_oid shows it, and it is never compared.
func TestResource_connectorOIDComputedNotCompared(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_resource.csv"
	cfg := resourceConfig(loginOnly, "")
	stampConnector := func(oid string) func() {
		return func() {
			b := stubBody(s, "resources", stubResourceOID)
			b["connectorRef"].(map[string]any)["oid"] = oid
			s.Put("resources", b)
		}
	}
	const otherEnvConnector = "c0000000-0000-4000-8000-0000000000c2"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: stampConnector(stubConnectorOID),
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("connector_oid"), knownvalue.StringExact(stubConnectorOID)),
				},
			},
			{
				PreConfig: stampConnector(otherEnvConnector),
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("connector_oid"), knownvalue.StringExact(otherEnvConnector)),
				},
				Check: checkStub(func() error {
					if n := len(requests(s, "PATCH")); n != 0 {
						return fmt.Errorf("want no PATCH, got %d", n)
					}
					return nil
				}),
			},
			// A changed connector version is a change of connectorRef: the
			// filter is replaced and re-resolved, and connector_oid is
			// unknown until then.
			{
				Config: strings.Replace(cfg, `version = "2.8"`, `version = "2.9"`, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("connector_oid")),
					},
				},
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					if got := patches[0].Query["options"]; !reflect.DeepEqual(got, []string{"reevaluateSearchFilters"}) {
						return fmt.Errorf("update options = %v", got)
					}
					want := asJSON(t, map[string]any{"objectModification": map[string]any{"itemDelta": []any{map[string]any{
						"modificationType": "replace", "path": "connectorRef",
						"value": map[string]any{"type": "ConnectorType", "relation": "org:default", "filter": map[string]any{
							"text": `connectorType = "` + csvConnectorType + `" and connectorVersion = "2.9"`,
						}},
					}}}})
					if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, want) {
						return fmt.Errorf("PATCH body = %v, want %v", got, want)
					}
					return nil
				}),
			},
		},
	})
}

// Server-managed parts of a resource (native capabilities, the cached
// schema, the operational state) never reach state and are no drift; the
// configured capabilities are compared.
func TestResource_nativeCapabilitiesIgnored(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_resource.csv"
	cfg := resourceConfig(loginOnly, `  capabilities = { create = { enabled = false } }`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					b := stubBody(s, "resources", stubResourceOID)
					b["capabilities"] = map[string]any{
						"cachingMetadata": map[string]any{"retrievalTimestamp": "2026-10-22T16:11:04.750Z", "serialNumber": "1"},
						"configured":      map[string]any{"create": map[string]any{"enabled": false}},
						"native": map[string]any{
							"create": map[string]any{}, "read": map[string]any{"returnDefaultAttributesOption": false},
							"script": map[string]any{"host": []any{map[string]any{"type": "resource"}}},
						},
					}
					b["schema"] = map[string]any{
						"cachingMetadata": map[string]any{"retrievalTimestamp": "2026-10-22T16:11:04.756Z", "serialNumber": "2"},
						"definition":      map[string]any{"@ns": xsdNamespace, "schema": "<xsd:schema/>"},
					}
					b["operationalState"] = map[string]any{"lastAvailabilityStatus": "up"}
					b["operationalStateHistory"] = []any{map[string]any{"lastAvailabilityStatus": "up"}}
					s.Put("resources", b)
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("capabilities"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"create": knownvalue.ObjectExact(map[string]knownvalue.Check{"enabled": knownvalue.Bool(false)}),
					})),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("schema"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("extra"), knownvalue.Null()),
				},
			},
			// A configured capability changed in midPoint is drift.
			{
				PreConfig: func() {
					b := stubBody(s, "resources", stubResourceOID)
					b["capabilities"].(map[string]any)["configured"] = map[string]any{"create": map[string]any{"enabled": true}}
					s.Put("resources", b)
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestResource_schemaHandlingAttributeAddIsNarrowDelta(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: resourceConfig(loginOnly, "")},
			{
				Config: resourceConfig(loginAndFullName, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("midpoint_resource.csv", plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					if got, want := patches[0].Query["options"], []string{"reevaluateSearchFilters"}; !reflect.DeepEqual(got, want) {
						return fmt.Errorf("update options = %v, want %v", got, want)
					}
					objectType := asList(stubBody(s, "resources", stubResourceOID)["schemaHandling"].(map[string]any)["objectType"])[0].(map[string]any)
					want := asJSON(t, map[string]any{"objectModification": map[string]any{"itemDelta": []any{map[string]any{
						"modificationType": "add",
						"path":             fmt.Sprintf("schemaHandling/objectType[%v]/attribute", objectType["@id"]),
						"value": []any{map[string]any{"ref": "ri:fullName", "inbound": []any{
							map[string]any{"target": map[string]any{"path": "fullName"}},
						}}},
					}}}})
					if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, want) {
						return fmt.Errorf("PATCH body = %v, want %v", got, want)
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
	})
}

// A mapping edited in midPoint (the GUI) is drift and is reverted narrowly.
func TestResource_mappingEditIsDrift(t *testing.T) {
	s := newTestStub(t)
	cfg := resourceConfig(loginOnly, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					b := stubBody(s, "resources", stubResourceOID)
					ot := asList(b["schemaHandling"].(map[string]any)["objectType"])[0].(map[string]any)
					attr := asList(ot["attribute"])[0].(map[string]any)
					asList(attr["inbound"])[0].(map[string]any)["target"] = map[string]any{"path": "nickName"}
					s.Put("resources", b)
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					deltas := asList(patches[0].Body["objectModification"].(map[string]any)["itemDelta"])
					if len(deltas) != 1 {
						return fmt.Errorf("want one item delta, got %v", deltas)
					}
					d := deltas[0].(map[string]any)
					if d["modificationType"] != "replace" || !regexp.MustCompile(`^schemaHandling/objectType\[\d+\]/attribute\[\d+\]/inbound\[\d+\]/target$`).MatchString(fmt.Sprint(d["path"])) {
						return fmt.Errorf("delta = %v, want a replace of the inbound target", d)
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
	})
}

// A resource with shadows is not deleted unless force_destroy is set.
func TestResource_destroyRefusedWithShadows(t *testing.T) {
	s := newTestStub(t)
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: resourceConfig(loginOnly, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID("midpoint_resource.csv", &oid),
					checkStub(func() error {
						s.Put("shadows", map[string]any{"name": "jdoe", "resourceRef": map[string]any{"oid": stubResourceOID, "type": "ResourceType"}})
						return nil
					}),
				),
			},
			{
				Config:      resourceConfig(loginOnly, ""),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`still reference it`),
			},
			{Config: resourceConfig(loginOnly, `  force_destroy = true`)},
		},
		CheckDestroy: checkGone(s, "resources", &oid),
	})
}

// extra must not hold the items the typed attributes write.
func TestResource_extraConflicts(t *testing.T) {
	s := newTestStub(t)
	for _, item := range []string{"connectorRef", "capabilities", "schema", "schemaHandling"} {
		t.Run(item, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{{
					Config:      resourceConfig(loginOnly, fmt.Sprintf(`  extra = { %s = { x = 1 } }`, item)),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`Typed item in extra`),
				}},
			})
		})
	}
}
