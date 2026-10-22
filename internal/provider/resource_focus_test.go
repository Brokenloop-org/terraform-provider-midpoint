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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/jplana/terraform-provider-midpoint/internal/mpstub"
)

const (
	stubServiceOID = "5e000000-0000-4000-8000-0000000000a1"
	stubOtherOID   = "5e000000-0000-4000-8000-0000000000a2"
)

// stateOID returns the oid attribute of addr.
func stateOID(st *terraform.State, addr string) (string, error) {
	rs, ok := st.RootModule().Resources[addr]
	if !ok {
		return "", fmt.Errorf("%s not in state", addr)
	}
	return rs.Primary.Attributes["oid"], nil
}

// checkGone is a CheckDestroy that restType/oid of addr is gone.
func checkGone(s *mpstub.Server, restType string, oid *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if *oid == "" {
			return fmt.Errorf("oid not captured")
		}
		if s.Get(restType, *oid) != nil {
			return fmt.Errorf("%s/%s still exists", restType, *oid)
		}
		return nil
	}
}

// captureOID stores the oid of addr in *oid.
func captureOID(addr string, oid *string) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		var err error
		*oid, err = stateOID(st, addr)
		return err
	}
}

// onePost checks that exactly one POST went to path with options and
// returns its body.
func onePost(t *testing.T, s *mpstub.Server, path string, options []string) (map[string]any, error) {
	posts := requests(s, "POST")
	if len(posts) != 1 || posts[0].Path != path {
		return nil, fmt.Errorf("want one POST %s, got %+v", path, posts)
	}
	if got := posts[0].Query["options"]; !reflect.DeepEqual(got, options) {
		return nil, fmt.Errorf("create options = %v, want %v", got, options)
	}
	// The body is wrapped in its type, e.g. {"role": {...}}.
	for _, v := range asJSON(t, posts[0].Body).(map[string]any) {
		body, _ := v.(map[string]any)
		return body, nil
	}
	return nil, fmt.Errorf("empty POST body")
}

func refreshIsClean() resource.TestStep {
	return resource.TestStep{
		RefreshState: true,
		RefreshPlanChecks: resource.RefreshPlanChecks{
			PostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
		},
	}
}

func TestRole_lifecycle(t *testing.T) {
	s := newTestStub(t)
	s.Put("services", map[string]any{"oid": stubServiceOID, "name": "dev-k8s"})
	const addr = "midpoint_role.admin"
	cfg := func(desc string) string {
		return fmt.Sprintf(`
resource "midpoint_role" "admin" {
  name         = "dev-k8s / admin"
  display_name = "Admin"
  description  = %q
  identifier   = "admin"
  archetype    = %q

  inducement {
    target {
      oid  = %q
      type = "ServiceType"
    }
  }
}
`, desc, engArchetypeOID, stubServiceOID)
	}
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg("v1"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("archetype"), knownvalue.StringExact(engArchetypeOID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("assignment"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("inducement"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectExact(map[string]knownvalue.Check{
							"target": knownvalue.ObjectExact(map[string]knownvalue.Check{
								"oid": knownvalue.StringExact(stubServiceOID), "type": knownvalue.StringExact("ServiceType"),
								"relation": knownvalue.StringExact("org:default"), "filter": knownvalue.Null(),
							}),
							"construction": knownvalue.Null(), "order": knownvalue.Null(),
							"condition": knownvalue.Null(), "policy_rule": knownvalue.Null(), "extra": knownvalue.Null(),
						}),
					})),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("extra"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("activation"), knownvalue.Null()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					resource.TestCheckResourceAttrPair(addr, "id", addr, "oid"),
					checkStub(func() error {
						body, err := onePost(t, s, "/roles", []string{"isImport", "raw"})
						if err != nil {
							return err
						}
						want := asJSON(t, map[string]any{
							"name": "dev-k8s / admin", "displayName": "Admin", "description": "v1", "identifier": "admin",
							"assignment":        []any{map[string]any{"targetRef": archetypeRefValue(engArchetypeOID)}},
							"archetypeRef":      archetypeRefValue(engArchetypeOID),
							"roleMembershipRef": archetypeRefValue(engArchetypeOID),
							"inducement":        []any{map[string]any{"targetRef": map[string]any{"oid": stubServiceOID, "type": "ServiceType"}}},
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
		CheckDestroy: checkGone(s, "roles", &oid),
	})
}

func TestService_lifecycle(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_service.app"
	cfg := func(display string) string {
		return fmt.Sprintf(`
resource "midpoint_service" "app" {
  oid          = %q
  name         = "dev-k8s"
  display_name = %q
  archetype    = %q
  requestable  = true
  authorization = [{ action = ["urn:test#read"] }]
}
`, stubServiceOID, display, engArchetypeOID)
	}
	oid := stubServiceOID
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg("Dev cluster"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "oid", stubServiceOID),
					checkStub(func() error {
						body, err := onePost(t, s, "/services", []string{"isImport", "raw"})
						if err != nil {
							return err
						}
						if body["oid"] != stubServiceOID || body["archetypeRef"] == nil || body["requestable"] != true {
							return fmt.Errorf("POST body = %v", body)
						}
						return nil
					}),
				),
			},
			{Config: cfg("Dev cluster app")},
			refreshIsClean(),
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		},
		CheckDestroy: checkGone(s, "services", &oid),
	})
}

func TestOrg_lifecycle(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_org.tenant"
	cfg := `
resource "midpoint_org" "tenant" {
  name      = "tenant-a"
  tenant    = true
  archetype = "` + engArchetypeOID + `"
}
`
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					resource.TestCheckResourceAttr(addr, "tenant", "true"),
					checkStub(func() error {
						// Not raw: midPoint computes the archetype references.
						body, err := onePost(t, s, "/orgs", []string{"isImport"})
						if err != nil {
							return err
						}
						if body["tenant"] != true || body["archetypeRef"] != nil || body["roleMembershipRef"] != nil {
							return fmt.Errorf("POST body = %v", body)
						}
						return nil
					}),
				),
			},
			refreshIsClean(),
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		},
		CheckDestroy: checkGone(s, "orgs", &oid),
	})
}

func TestArchetype_lifecycle(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_archetype.cluster"
	cfg := func(label string) string {
		return fmt.Sprintf(`
resource "midpoint_archetype" "cluster" {
  name             = "Cluster"
  holder_type      = ["RoleType"]
  archetype_policy = { display = { label = %q } }
}
`, label)
	}
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg("Cluster"),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					checkStub(func() error {
						body, err := onePost(t, s, "/archetypes", []string{"isImport", "raw"})
						if err != nil {
							return err
						}
						want := asJSON(t, map[string]any{
							"name": "Cluster",
							"assignment": []any{map[string]any{
								"identifier": "holderType", "assignmentRelation": []any{map[string]any{"holderType": []any{"#RoleType"}}},
							}},
							"archetypePolicy": map[string]any{"display": map[string]any{"label": "Cluster"}},
						})
						if !reflect.DeepEqual(body, want) {
							return fmt.Errorf("POST body = %v, want %v", body, want)
						}
						return nil
					}),
				),
			},
			{Config: cfg("Clusters")},
			refreshIsClean(),
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		},
		CheckDestroy: checkGone(s, "archetypes", &oid),
	})
}

func roleWithInducements(targets ...string) string {
	cfg := `
resource "midpoint_role" "r" {
  name = "access"
`
	for _, oid := range targets {
		cfg += fmt.Sprintf(`
  inducement {
    target {
      oid  = %q
      type = "ServiceType"
    }
  }
`, oid)
	}
	return cfg + "}\n"
}

func TestRole_updateInducementSendsAddOnly(t *testing.T) {
	s := newTestStub(t)
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: roleWithInducements(stubServiceOID), Check: captureOID("midpoint_role.r", &oid)},
			{
				Config: roleWithInducements(stubServiceOID, stubOtherOID),
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					want := asJSON(t, map[string]any{"objectModification": map[string]any{"itemDelta": []any{
						map[string]any{"modificationType": "add", "path": "inducement", "value": []any{
							map[string]any{"targetRef": map[string]any{"oid": stubOtherOID, "type": "ServiceType", "relation": "org:default"}},
						}},
					}}})
					if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, want) {
						return fmt.Errorf("PATCH body = %v, want %v", got, want)
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
		CheckDestroy: checkGone(s, "roles", &oid),
	})
}

func TestRole_outOfBandInducementIsDrift(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_role.r"
	cfg := roleWithInducements(stubServiceOID)
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg, Check: captureOID(addr, &oid)},
			{
				PreConfig: func() {
					body := s.Get("roles", oid)
					body["inducement"] = append(asList(body["inducement"]), map[string]any{
						"targetRef": map[string]any{"oid": stubOtherOID, "type": "RoleType"},
					})
					s.Put("roles", body)
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					deltas := asJSON(t, patches[0].Body).(map[string]any)["objectModification"].(map[string]any)["itemDelta"].([]any)
					if len(deltas) != 1 || deltas[0].(map[string]any)["modificationType"] != "delete" ||
						deltas[0].(map[string]any)["path"] != "inducement" {
						return fmt.Errorf("want one inducement delete, got %v", deltas)
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
		CheckDestroy: checkGone(s, "roles", &oid),
	})
}

func TestRole_archetypeConflictIsInvalid(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "midpoint_role" "r" {
  name      = "x"
  archetype = %q
  assignment {
    target {
      oid  = %q
      type = "ArchetypeType"
    }
  }
}
`, engArchetypeOID, engArchetypeOID),
			ExpectError: regexp.MustCompile(`Conflicting archetype`),
		}},
	})
	if len(s.Requests()) != 0 {
		t.Errorf("an invalid configuration must not reach midPoint, got %+v", s.Requests())
	}
}
