package provider

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const stubTemplateOID = "07000000-0000-4000-8000-000000000001"

func TestObjectTemplate_lifecycle(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_object_template.user"
	cfg := func(desc string) string {
		return fmt.Sprintf(`
resource "midpoint_object_template" "user" {
  name            = "user template"
  description     = %q
  lifecycle_state = "active"
  mapping = [{
    name     = "full-name"
    strength = "weak"
    source   = [{ path = "givenName" }, { path = "familyName" }]
    target   = { path = "fullName" }
  }]
  item = [{
    ref         = "description"
    displayName = "Description"
  }]
  iteration_specification = { maxIterations = 5 }
  extra = { documentation = "managed by terraform" }
}
`, desc)
	}
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg("v1"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("name"), knownvalue.StringExact("user template")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("lifecycle_state"), knownvalue.StringExact("active")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("extra"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"documentation": knownvalue.StringExact("managed by terraform"),
					})),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					resource.TestCheckResourceAttrPair(addr, "id", addr, "oid"),
					checkStub(func() error {
						body, err := onePost(t, s, "/objectTemplates", []string{"isImport", "raw"})
						if err != nil {
							return err
						}
						want := asJSON(t, map[string]any{
							"name": "user template", "description": "v1", "lifecycleState": "active",
							"mapping": []any{map[string]any{
								"name": "full-name", "strength": "weak",
								"source": []any{map[string]any{"path": "givenName"}, map[string]any{"path": "familyName"}},
								"target": map[string]any{"path": "fullName"},
							}},
							"item":                   []any{map[string]any{"ref": "description", "displayName": "Description"}},
							"iterationSpecification": map[string]any{"maxIterations": 5},
							"documentation":          "managed by terraform",
						})
						if !reflect.DeepEqual(body, want) {
							return fmt.Errorf("POST body = %v, want %v", body, want)
						}
						return nil
					}),
				),
			},
			{
				Config: cfg("v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					if got := patches[0].Query["options"]; !reflect.DeepEqual(got, []string{"raw"}) {
						return fmt.Errorf("update options = %v, want [raw]", got)
					}
					want := asJSON(t, map[string]any{"objectModification": map[string]any{"itemDelta": []any{
						map[string]any{"modificationType": "replace", "path": "description", "value": "v2"},
					}}})
					if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, want) {
						return fmt.Errorf("PATCH body = %v, want %v", got, want)
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
				Config:          cfg("v2"),
				ResourceName:    addr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
		},
		CheckDestroy: checkGone(s, "objectTemplates", &oid),
	})
}

// A single mapping object is written as a one-element list and kept as
// configured; a change made in midPoint shows as drift in list form.
func TestObjectTemplate_singleMappingIsAList(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_object_template.user"
	cfg := fmt.Sprintf(`
resource "midpoint_object_template" "user" {
  oid  = %q
  name = "single mapping"
  mapping = {
    name   = "full-name"
    target = { path = "fullName" }
  }
}
`, stubTemplateOID)
	mapping := map[string]any{"name": "full-name", "target": map[string]any{"path": "fullName"}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("mapping"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"name":   knownvalue.StringExact("full-name"),
						"target": knownvalue.ObjectExact(map[string]knownvalue.Check{"path": knownvalue.StringExact("fullName")}),
					})),
				},
				Check: checkStub(func() error {
					body, err := onePost(t, s, "/objectTemplates", []string{"isImport", "raw"})
					if err != nil {
						return err
					}
					if got, want := body["mapping"], asJSON(t, []any{mapping}); !reflect.DeepEqual(got, want) {
						return fmt.Errorf("POST mapping = %v, want %v", got, want)
					}
					return nil
				}),
			},
			refreshIsClean(),
			{
				PreConfig: func() {
					b := stubBody(s, "objectTemplates", stubTemplateOID)
					b["mapping"] = []any{mapping, map[string]any{"name": "added-in-gui"}}
					s.Put("objectTemplates", b)
				},
				RefreshState: true,
				RefreshPlanChecks: resource.RefreshPlanChecks{
					PostRefresh: []plancheck.PlanCheck{plancheck.ExpectNonEmptyPlan()},
				},
				ExpectNonEmptyPlan: true,
				Check:              resource.TestCheckResourceAttr(addr, "mapping.#", "2"),
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
			},
			refreshIsClean(),
		},
	})
}

func TestObjectTemplate_rejectsTypedItemsInExtra(t *testing.T) {
	s := newTestStub(t)
	for _, tc := range []struct{ extra, want string }{
		{`{ description = "x" }`, `description is a typed attribute`},
		{`{ mapping = [] }`, `mapping is a typed attribute`},
		{`{ iterationSpecification = {} }`, `iterationSpecification is a typed attribute`},
		{`{ oid = "x" }`, `extra must not hold oid`},
		{`"text"`, `extra must be an object`},
	} {
		t.Run(tc.extra, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{{
					Config: fmt.Sprintf(`
resource "midpoint_object_template" "t" {
  name  = "t"
  extra = %s
}
`, tc.extra),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}
