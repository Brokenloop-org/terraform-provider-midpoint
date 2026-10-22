package provider

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestMark_lifecycle(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_mark.protected"
	cfg := func(label string) string {
		return fmt.Sprintf(`
resource "midpoint_mark" "protected" {
  name        = "protected"
  description = "Objects nobody may touch."
  display = {
    label = %q
    icon  = { cssClass = "fa fa-lock" }
  }
  event = { enabledByDefault = true }
  extra = { documentation = "mark doc" }
}
`, label)
	}
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg("Protected"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("event"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"enabledByDefault": knownvalue.Bool(true),
					})),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					resource.TestCheckResourceAttrPair(addr, "id", addr, "oid"),
					checkStub(func() error {
						body, err := onePost(t, s, "/marks", []string{"isImport"})
						if err != nil {
							return err
						}
						want := asJSON(t, map[string]any{
							"name": "protected", "description": "Objects nobody may touch.",
							"display":       map[string]any{"label": "Protected", "icon": map[string]any{"cssClass": "fa fa-lock"}},
							"eventMark":     map[string]any{"enabledByDefault": true},
							"documentation": "mark doc",
						})
						if !reflect.DeepEqual(body, want) {
							return fmt.Errorf("POST body = %v, want %v", body, want)
						}
						return nil
					}),
				),
			},
			{
				Config: cfg("Protected object"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					if got := patches[0].Query["options"]; len(got) != 0 {
						return fmt.Errorf("update options = %v, want none", got)
					}
					display, _ := stubBody(s, "marks", oid)["display"].(map[string]any)
					if display["label"] != "Protected object" {
						return fmt.Errorf("stored display = %v", display)
					}
					return nil
				}),
			},
			refreshIsClean(),
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config:          cfg("Protected object"),
				ResourceName:    addr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
		},
		CheckDestroy: checkGone(s, "marks", &oid),
	})
}

// A label changed in midPoint shows as drift on display and is reverted.
func TestMark_displayDrift(t *testing.T) {
	s := newTestStub(t)
	const markOID = "07000000-0000-4000-8000-000000000002"
	cfg := fmt.Sprintf(`
resource "midpoint_mark" "m" {
  oid     = %q
  name    = "m"
  display = { label = "Mine" }
}
`, markOID)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					b := stubBody(s, "marks", markOID)
					b["display"] = map[string]any{"label": map[string]any{"orig": "Theirs", "norm": "theirs"}}
					s.Put("marks", b)
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				Check: checkStub(func() error {
					display, _ := stubBody(s, "marks", markOID)["display"].(map[string]any)
					if display["label"] != "Mine" {
						return fmt.Errorf("stored display = %v, want label Mine", display)
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
	})
}
