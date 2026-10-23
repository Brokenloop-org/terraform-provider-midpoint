package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/normalize"
)

// simpleRestTypes maps midpoint_object_template, midpoint_mark and
// midpoint_task to REST types.
var simpleRestTypes = map[string]string{
	"midpoint_object_template": "objectTemplates",
	"midpoint_mark":            "marks",
	"midpoint_task":            "tasks",
}

// accSimpleGone is a CheckDestroy that every template, mark and task in the
// state is gone from the server.
func accSimpleGone(c *client.Client) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		for _, rs := range st.RootModule().Resources {
			typ, ok := simpleRestTypes[rs.Type]
			if !ok {
				continue
			}
			body, err := accGet(c, typ, rs.Primary.Attributes["oid"])
			if err != nil {
				return err
			}
			if body != nil {
				return fmt.Errorf("%s/%s still exists", typ, rs.Primary.Attributes["oid"])
			}
		}
		return nil
	}
}

// accSimpleItem checks one canonical top-level item of the object at addr;
// want nil means absent.
func accSimpleItem(c *client.Client, addr, item string, want any) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		rs, ok := st.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		body, err := accGet(c, simpleRestTypes[rs.Type], rs.Primary.Attributes["oid"])
		if err != nil {
			return err
		}
		if body == nil {
			return fmt.Errorf("%s does not exist in midPoint", addr)
		}
		got := stripIDs(body[item])
		if !normalize.Equal(got, normalize.Canonical(simpleRestTypes[rs.Type], map[string]any{item: want})[item]) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(want)
			return fmt.Errorf("%s %s = %s, want %s", addr, item, gj, wj)
		}
		return nil
	}
}

func TestAccObjectTemplate_mapping(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-tpl")
	// asList writes the multi-valued mapping and expression script as
	// one-element lists, the form a read and an import produce; otherwise
	// each is the single object.
	cfgAs := func(strength string, asList bool) string {
		open, closing, item := "", "", ""
		if asList {
			open, closing, item = "[", "]", "- "
		}
		return fmt.Sprintf(`
resource "midpoint_object_template" "user" {
  name            = %q
  description     = "Acceptance user template"
  lifecycle_state = "active"
  mapping = %syamldecode(<<-EOT
    name: full-name
    strength: %s
    source:
      - path: givenName
      - path: familyName
    expression:
      script:
        %scode: "basic.concatName(givenName, familyName)"
    target:
      path: fullName
  EOT
  )%s
  iteration_specification = { maxIterations = 5 }
}
`, name, open, strength, item, closing)
	}
	cfg := func(strength string) string { return cfgAs(strength, false) }
	const addr = "midpoint_object_template.user"
	var oid, version string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accSimpleGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg("weak"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("extra"), knownvalue.Null()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					accSimpleItem(c, addr, "lifecycleState", "active"),
					accSimpleItem(c, addr, "iterationSpecification", map[string]any{"maxIterations": json.Number("5")}),
					accSimpleItem(c, addr, "mapping", []any{map[string]any{
						"name": "full-name", "strength": "weak",
						"source":     []any{map[string]any{"path": "givenName"}, map[string]any{"path": "familyName"}},
						"expression": map[string]any{"script": map[string]any{"code": "basic.concatName(givenName, familyName)"}},
						"target":     map[string]any{"path": "fullName"},
					}}),
				),
			},
			emptyPlanAfterRefresh(),
			{
				Config: cfg("strong"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: func(st *terraform.State) error {
					body, err := accGet(c, "objectTemplates", st.RootModule().Resources[addr].Primary.Attributes["oid"])
					if err != nil {
						return err
					}
					m, _ := asList(body["mapping"])[0].(map[string]any)
					if len(asList(body["mapping"])) != 1 || m["strength"] != "strong" {
						return fmt.Errorf("mapping = %v, want one strong mapping", body["mapping"])
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
			{
				// The list form is the same mapping: nothing is sent.
				PreConfig: func() { version = accVersion(t, c, "objectTemplates", oid) },
				Config:    cfgAs("strong", true),
				Check: func(*terraform.State) error {
					if v := accVersion(t, c, "objectTemplates", oid); v != version {
						return fmt.Errorf("version = %s after the list-form apply, want %s (no write)", v, version)
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
			// Imported, the mapping reads back as a list: the list-form
			// state verifies, and an import block with the list-form
			// configuration plans no change.
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
			{
				Config:          cfgAs("strong", true),
				ResourceName:    addr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
		},
	})
}

func TestAccMark_display(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-mark")
	cfg := func(label string) string {
		return fmt.Sprintf(`
resource "midpoint_mark" "m" {
  name        = %q
  description = "Acceptance mark"
  display = {
    label = %q
    icon  = { cssClass = "fa fa-lock", color = "red" }
  }
  event = { enabledByDefault = false }
}
`, name, label)
	}
	const addr = "midpoint_mark.m"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accSimpleGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg("Protected"),
				Check: resource.ComposeAggregateTestCheckFunc(
					accSimpleItem(c, addr, "display", map[string]any{
						"label": "Protected", "icon": map[string]any{"cssClass": "fa fa-lock", "color": "red"},
					}),
					accSimpleItem(c, addr, "eventMark", map[string]any{"enabledByDefault": false}),
				),
			},
			emptyPlanAfterRefresh(),
			{
				Config: cfg("Protected object"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: accSimpleItem(c, addr, "display", map[string]any{
					"label": "Protected object", "icon": map[string]any{"cssClass": "fa fa-lock", "color": "red"},
				}),
			},
			emptyPlanAfterRefresh(),
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
			{
				Config:          cfg("Protected object"),
				ResourceName:    addr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
		},
	})
}

// accVersion returns the version of restType/oid; it fails the test when
// the object cannot be read.
func accVersion(t *testing.T, c *client.Client, restType, oid string) string {
	t.Helper()
	obj, err := c.Get(context.Background(), restType, oid, client.GetOptions{})
	if err != nil {
		t.Fatalf("reading %s/%s: %v", restType, oid, err)
	}
	v, _ := obj.Body["version"].(string)
	return v
}
