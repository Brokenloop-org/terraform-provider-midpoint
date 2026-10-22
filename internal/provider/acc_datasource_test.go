package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// cleanBody checks that a body holds name and none of the root oid,
// container ids or the server-managed items midPoint adds.
type cleanBody struct{ name string }

func (c cleanBody) CheckValue(v any) error {
	body, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("body is %T, want an object", v)
	}
	if body["name"] != c.name {
		return fmt.Errorf("body name = %v, want %q", body["name"], c.name)
	}
	for _, k := range []string{"oid", "version", "metadata", "iteration", "iterationToken", "operationExecution"} {
		if _, ok := body[k]; ok {
			return fmt.Errorf("body holds the server-managed item %q", k)
		}
	}
	var found error
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, e := range t {
				if k == "@id" || k == "@metadata" {
					found = fmt.Errorf("body holds %q", k)
				}
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(body)
	return found
}

func (c cleanBody) String() string { return "clean body named " + c.name }

func TestAccDataObject_builtinArchetype(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "midpoint_object" "by_name" {
  type = "archetypes"
  name = "Application"
}

data "midpoint_object" "by_oid" {
  type = "archetypes"
  oid  = data.midpoint_object.by_name.oid
}
`,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.midpoint_object.by_name", tfjsonpath.New("oid"), knownvalue.StringExact(accApplicationArchetype)),
				statecheck.ExpectKnownValue("data.midpoint_object.by_name", tfjsonpath.New("id"), knownvalue.StringExact(accApplicationArchetype)),
				statecheck.ExpectKnownValue("data.midpoint_object.by_name", tfjsonpath.New("body"), cleanBody{"Application"}),
				statecheck.ExpectKnownValue("data.midpoint_object.by_oid", tfjsonpath.New("name"), knownvalue.StringExact("Application")),
				statecheck.ExpectKnownValue("data.midpoint_object.by_oid", tfjsonpath.New("body"), cleanBody{"Application"}),
			},
		}},
	})
}

func TestAccDataConnector_csv(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "midpoint_connector" "csv" {
  connector_type = "` + accCSVConnectorType + `"
}
`,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.midpoint_connector.csv", tfjsonpath.New("oid"), knownvalue.StringRegexp(uuidPattern)),
					statecheck.ExpectKnownValue("data.midpoint_connector.csv", tfjsonpath.New("connector_version"), knownvalue.StringRegexp(regexp.MustCompile(`.`))),
					statecheck.ExpectKnownValue("data.midpoint_connector.csv", tfjsonpath.New("name"), knownvalue.StringRegexp(regexp.MustCompile(`CsvConnector`))),
				},
				Check: resource.TestCheckResourceAttrPair("data.midpoint_connector.csv", "id", "data.midpoint_connector.csv", "oid"),
			},
			{
				// The version just found selects the same connector.
				Config: `
data "midpoint_connector" "csv" {
  connector_type = "` + accCSVConnectorType + `"
}

data "midpoint_connector" "pinned" {
  connector_type    = data.midpoint_connector.csv.connector_type
  connector_version = data.midpoint_connector.csv.connector_version
}
`,
				Check: resource.TestCheckResourceAttrPair("data.midpoint_connector.pinned", "oid", "data.midpoint_connector.csv", "oid"),
			},
			{
				Config: `
data "midpoint_connector" "csv" {
  connector_type    = "` + accCSVConnectorType + `"
  connector_version = "0.0-missing"
}
`,
				ExpectError: regexp.MustCompile(`(?s)no\s+connector\s+of\s+type.*available\s+versions`),
			},
		},
	})
}
