package provider

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sync/atomic"
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
	objRoleOID  = "aaaaaaaa-0000-4000-8000-000000000001"
	objOtherOID = "aaaaaaaa-0000-4000-8000-000000000002"
	objAddr     = "midpoint_object.test"
)

// objectConfig is a midpoint_object "test" with the given attribute lines
// (type, oid, body, ...).
func objectConfig(attrs string) string {
	return fmt.Sprintf(`
resource "midpoint_object" "test" {
%s
}
`, attrs)
}

// checkStub runs fn against the stub as a TestCheckFunc.
func checkStub(fn func() error) resource.TestCheckFunc {
	return func(*terraform.State) error { return fn() }
}

// stubBody returns the stored body of restType/oid as plain JSON values.
func stubBody(s *mpstub.Server, restType, oid string) map[string]any {
	b := s.Get(restType, oid)
	if b == nil {
		return nil
	}
	raw, _ := json.Marshal(b)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// removedStep stops managing addr with a removed block (destroy = false),
// the way out for an adopted object, and checks that restType/oid is still
// in the stub and that no DELETE was sent.
func removedStep(s *mpstub.Server, addr, restType, oid string) resource.TestStep {
	return resource.TestStep{
		Config: fmt.Sprintf(`
removed {
  from = %s

  lifecycle {
    destroy = false
  }
}
`, addr),
		Check: checkStub(func() error {
			if s.Get(restType, oid) == nil {
				return fmt.Errorf("%s/%s was deleted", restType, oid)
			}
			if deletes := requests(s, "DELETE"); len(deletes) != 0 {
				return fmt.Errorf("removing an adopted object from state sent %+v", deletes)
			}
			return nil
		}),
	}
}

// adoptedDeleteRefused is the error a destroy of the adopted restType/oid
// fails with.
func adoptedDeleteRefused(restType, oid string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(restType+"/"+oid) + `\s+was\s+adopted\s+from\s+midPoint;\s+refusing\s+to\s+delete\s+it\.\s+Remove\s+it\s+from\s+state\s+with\s+a\s+` +
		"`" + `removed\s+\{\s+lifecycle\s+\{\s+destroy\s+=\s+false\s+\}\s+\}` + "`" + `\s+block,\s+or\s+set\s+force_destroy\s+=\s+true\s+to\s+delete\s+it\s+anyway\.`)
}

func TestObject_createReadDelete(t *testing.T) {
	s := newTestStub(t)
	cfg := objectConfig(`
  type = "roles"
  body = {
    name        = "Generic role"
    description = "created by midpoint_object"
  }
`)
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: cfg,
			ConfigStateChecks: []statecheck.StateCheck{
				// Only the configured body is in state: the stub's metadata,
				// version, activation and so on are not.
				statecheck.ExpectKnownValue(objAddr, tfjsonpath.New("body"), knownvalue.ObjectExact(map[string]knownvalue.Check{
					"name":        knownvalue.StringExact("Generic role"),
					"description": knownvalue.StringExact("created by midpoint_object"),
				})),
				statecheck.ExpectKnownValue(objAddr, tfjsonpath.New("name"), knownvalue.StringExact("Generic role")),
				statecheck.ExpectKnownValue(objAddr, tfjsonpath.New("adopt_existing"), knownvalue.Bool(false)),
				statecheck.ExpectKnownValue(objAddr, tfjsonpath.New("force_destroy"), knownvalue.Bool(false)),
				statecheck.ExpectKnownValue(objAddr, tfjsonpath.New("options"), knownvalue.Null()),
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet(objAddr, "oid"),
				resource.TestCheckResourceAttrPair(objAddr, "id", objAddr, "oid"),
				func(st *terraform.State) error {
					oid = st.RootModule().Resources[objAddr].Primary.Attributes["oid"]
					if s.Get("roles", oid) == nil {
						return fmt.Errorf("role %s not in the stub", oid)
					}
					posts := requests(s, "POST")
					if len(posts) != 1 || posts[0].Path != "/roles" {
						return fmt.Errorf("want one POST /roles, got %+v", posts)
					}
					if got := posts[0].Query["options"]; !reflect.DeepEqual(got, []string{"isImport"}) {
						return fmt.Errorf("create options = %v, want [isImport]", got)
					}
					return nil
				},
			),
		}},
		CheckDestroy: func(*terraform.State) error {
			if s.Get("roles", oid) != nil {
				return fmt.Errorf("role %s still exists", oid)
			}
			return nil
		},
	})
}

func TestObject_updateSendsDelta(t *testing.T) {
	s := newTestStub(t)
	cfg := func(desc string) string {
		return objectConfig(fmt.Sprintf(`
  type = "roles"
  oid  = %q
  body = {
    name        = "Delta role"
    description = %q
    riskLevel   = "low"
  }
`, objRoleOID, desc))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg("before")},
			{
				Config: cfg("after"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(objAddr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want exactly one PATCH, got %d", len(patches))
					}
					want := map[string]any{"objectModification": map[string]any{"itemDelta": []any{
						map[string]any{"modificationType": "replace", "path": "description", "value": "after"},
					}}}
					if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, asJSON(t, want)) {
						return fmt.Errorf("PATCH body = %v, want %v", got, want)
					}
					if got := stubBody(s, "roles", objRoleOID)["description"]; got != "after" {
						return fmt.Errorf("stored description = %v", got)
					}
					return nil
				}),
			},
		},
	})
}

func TestObject_driftShowsInPlan(t *testing.T) {
	s := newTestStub(t)
	cfg := objectConfig(fmt.Sprintf(`
  type = "roles"
  oid  = %q
  body = {
    name        = "Drift role"
    description = "managed"
  }
`, objRoleOID))
	outOfBand := func() {
		s.Put("roles", map[string]any{
			"oid": objRoleOID, "name": "Drift role", "description": "changed in the GUI", "riskLevel": "high",
		})
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig:          outOfBand,
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(objAddr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					got := stubBody(s, "roles", objRoleOID)
					want := map[string]any{"oid": objRoleOID, "name": "Drift role", "description": "managed"}
					if !reflect.DeepEqual(got, want) {
						return fmt.Errorf("stored role = %v, want %v", got, want)
					}
					return nil
				}),
			},
			{
				RefreshState: true,
				RefreshPlanChecks: resource.RefreshPlanChecks{
					PostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestObject_import(t *testing.T) {
	s := newTestStub(t)
	s.Put("roles", map[string]any{"oid": objOtherOID, "name": "Induced role"})
	cfg := objectConfig(fmt.Sprintf(`
  type = "roles"
  oid  = %q
  body = {
    name        = "Import role"
    description = "imported"
    inducement = [{
      order     = 2
      targetRef = { oid = %q, type = "RoleType", relation = "org:default" }
    }]
  }
`, objRoleOID, objOtherOID))
	importID := "roles/" + objRoleOID
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				ResourceName:      objAddr,
				ImportState:       true,
				ImportStateId:     importID,
				ImportStateVerify: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("want one imported instance, got %d", len(states))
					}
					a := states[0].Attributes
					for k, want := range map[string]string{
						"type": "roles", "oid": objRoleOID, "id": objRoleOID, "name": "Import role",
						"adopt_existing": "false", "force_destroy": "false",
					} {
						if a[k] != want {
							return fmt.Errorf("imported %s = %q, want %q", k, a[k], want)
						}
					}
					return nil
				},
			},
			{
				// An import block with the same HCL plans no change.
				Config:          cfg,
				ResourceName:    objAddr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
				ImportStateId:   importID,
			},
		},
	})
}

func TestObject_adoptExisting(t *testing.T) {
	s := newTestStub(t)
	s.Put("roles", map[string]any{"oid": objRoleOID, "name": "Built-in", "description": "original", "riskLevel": "high"})
	cfg := objectConfig(fmt.Sprintf(`
  type           = "roles"
  oid            = %q
  adopt_existing = true
  body = {
    name        = "Built-in"
    description = "customized"
  }
`, objRoleOID))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(objAddr, "oid", objRoleOID),
				resource.TestCheckResourceAttr(objAddr, "name", "Built-in"),
				checkStub(func() error {
					if posts := requests(s, "POST"); len(posts) != 0 {
						return fmt.Errorf("adoption must not POST, got %+v", posts)
					}
					if puts := requests(s, "PUT"); len(puts) != 0 {
						return fmt.Errorf("adoption must not PUT, got %+v", puts)
					}
					// Adoption sends exactly the configured diff: the changed
					// description and the removal of the unconfigured riskLevel.
					patches := requests(s, "PATCH")
					if len(patches) != 1 || patches[0].Path != "/roles/"+objRoleOID {
						return fmt.Errorf("want exactly one PATCH /roles/%s, got %+v", objRoleOID, patches)
					}
					wantPatch := map[string]any{"objectModification": map[string]any{"itemDelta": []any{
						map[string]any{"modificationType": "replace", "path": "description", "value": "customized"},
						map[string]any{"modificationType": "replace", "path": "riskLevel"},
					}}}
					if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, asJSON(t, wantPatch)) {
						return fmt.Errorf("PATCH body = %v, want %v", got, wantPatch)
					}
					got := stubBody(s, "roles", objRoleOID)
					want := map[string]any{"oid": objRoleOID, "name": "Built-in", "description": "customized"}
					if !reflect.DeepEqual(got, want) {
						return fmt.Errorf("stored role = %v, want %v", got, want)
					}
					return nil
				}),
			),
		},
			removedStep(s, objAddr, "roles", objRoleOID),
		},
	})
}

// TestObject_adoptedDeleteRefused: destroying an adopted object fails and
// sends nothing; with force_destroy it is deleted.
func TestObject_adoptedDeleteRefused(t *testing.T) {
	s := newTestStub(t)
	s.Put("roles", map[string]any{"oid": objRoleOID, "name": "Built-in"})
	cfg := func(force bool) string {
		return objectConfig(fmt.Sprintf(`
  type           = "roles"
  oid            = %q
  adopt_existing = true
  force_destroy  = %t
  body           = { name = "Built-in" }
`, objRoleOID, force))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg(false)},
			{
				Config:      cfg(false),
				Destroy:     true,
				ExpectError: adoptedDeleteRefused("roles", objRoleOID),
			},
			{
				Config: cfg(false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(objAddr, plancheck.ResourceActionNoop)},
				},
				Check: checkStub(func() error {
					if s.Get("roles", objRoleOID) == nil {
						return fmt.Errorf("the refused destroy deleted the role")
					}
					if deletes := requests(s, "DELETE"); len(deletes) != 0 {
						return fmt.Errorf("the refused destroy sent %+v", deletes)
					}
					return nil
				}),
			},
			{Config: cfg(true)},
		},
		CheckDestroy: func(*terraform.State) error {
			if s.Get("roles", objRoleOID) != nil {
				return fmt.Errorf("role still exists after a forced destroy")
			}
			return nil
		},
	})
}

func TestObject_existingOIDFails(t *testing.T) {
	s := newTestStub(t)
	s.Put("roles", map[string]any{"oid": objRoleOID, "name": "Taken"})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: objectConfig(fmt.Sprintf(`
  type = "roles"
  oid  = %q
  body = { name = "Mine" }
`, objRoleOID)),
			ExpectError: regexp.MustCompile(`roles/` + objRoleOID + ` already exists`),
		}},
	})
	if posts := requests(s, "POST"); len(posts) != 0 {
		t.Errorf("a create over an existing OID must not POST, got %+v", posts)
	}
	if got := stubBody(s, "roles", objRoleOID)["name"]; got != "Taken" {
		t.Errorf("existing role changed: name = %v", got)
	}
}

func TestObject_oidConflictsWithBody(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: objectConfig(fmt.Sprintf(`
  type = "roles"
  oid  = %q
  body = { name = "Mine", oid = %q }
`, objRoleOID, objOtherOID)),
			ExpectError: regexp.MustCompile(`(?s)body oid .* differs from the oid\s+attribute`),
		}},
	})
	if len(s.Requests()) != 0 {
		t.Errorf("an invalid configuration must not reach midPoint, got %+v", s.Requests())
	}
}

func TestObject_bodyMustBeObject(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: objectConfig(`
  type = "roles"
  body = ["not", "an", "object"]
`),
			ExpectError: regexp.MustCompile(`body must be an object`),
		}},
	})
}

func TestObject_forceDestroy(t *testing.T) {
	s := newTestStub(t)
	cfg := func(force bool) string {
		return objectConfig(fmt.Sprintf(`
  type          = "roles"
  oid           = %q
  force_destroy = %t
  body          = { name = "Referenced role" }
`, objRoleOID, force))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg(false)},
			{
				PreConfig: func() {
					s.Put("users", map[string]any{"name": "member", "assignment": []any{
						map[string]any{"targetRef": map[string]any{"oid": objRoleOID, "type": "RoleType"}},
					}})
				},
				Config:      cfg(false),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`still reference it`),
			},
			{Config: cfg(true)},
		},
		CheckDestroy: func(*terraform.State) error {
			if s.Get("roles", objRoleOID) != nil {
				return fmt.Errorf("role still exists after a forced destroy")
			}
			return nil
		},
	})
}

func TestObjectSpec(t *testing.T) {
	for _, tc := range []struct {
		typ         string
		members     string
		readOptions []string
	}{
		{"roles", "assignmentHolders", nil},
		{"services", "assignmentHolders", nil},
		{"orgs", "assignmentHolders", nil},
		{"archetypes", "assignmentHolders", nil},
		{"resources", "shadows", []string{"noFetch"}},
		{"functionLibraries", "", nil},
		{"users", "", nil},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			spec := objectSpec(tc.typ)
			if spec.RestType != tc.typ || spec.ShortName != "object" {
				t.Errorf("spec = %+v", spec)
			}
			if !reflect.DeepEqual(spec.CreateOptions, []string{"isImport"}) || spec.UpdateOptions != nil {
				t.Errorf("options = %v / %v", spec.CreateOptions, spec.UpdateOptions)
			}
			if !reflect.DeepEqual(spec.ReadOptions, tc.readOptions) {
				t.Errorf("read options = %v, want %v", spec.ReadOptions, tc.readOptions)
			}
			members := ""
			if spec.MembersCheck != nil {
				members, _ = spec.MembersCheck("x")
			}
			if members != tc.members {
				t.Errorf("members check type = %q, want %q", members, tc.members)
			}
		})
	}
}

func TestObject_bodyOIDChangeReplaces(t *testing.T) {
	s := newTestStub(t)
	cfg := func(oid string) string {
		return objectConfig(fmt.Sprintf(`
  type = "functionLibraries"
  body = { name = "lib", oid = %q }
`, oid))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg(objRoleOID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(objAddr, tfjsonpath.New("oid"), knownvalue.StringExact(objRoleOID)),
						plancheck.ExpectKnownValue(objAddr, tfjsonpath.New("name"), knownvalue.StringExact("lib")),
					},
				},
			},
			{
				Config: cfg(objOtherOID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(objAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.TestCheckResourceAttr(objAddr, "oid", objOtherOID),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			for _, oid := range []string{objRoleOID, objOtherOID} {
				if s.Get("functionLibraries", oid) != nil {
					return fmt.Errorf("%s still exists", oid)
				}
			}
			return nil
		},
	})
}

// failReadBack makes the first GET of restType/oid after a request with
// method after fail with status, once. fired reports whether it did.
func failReadBack(s *mpstub.Server, after, restType, oid string, status int) (fired func() bool) {
	var written, done atomic.Bool
	s.SetFault(func(r mpstub.Recorded) int {
		switch {
		case r.Method == after:
			written.Store(true)
		case written.Load() && !done.Load() && r.Method == "GET" && r.Path == "/"+restType+"/"+oid:
			done.Store(true)
			return status
		}
		return 0
	})
	return done.Load
}

// TestObject_readBackFailureDoesNotTaint: when the write succeeds but the
// read-back fails, the object stays in state untainted (a warning, not an
// error), so the next plan never replaces (deletes) it.
func TestObject_readBackFailureDoesNotTaint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		adopt  bool
		after  string
		status int
	}{
		{"create, server error", false, "POST", 500},
		{"create, not found", false, "POST", 404},
		{"adopt, server error", true, "PATCH", 500},
		{"adopt, not found", true, "PATCH", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStub(t)
			if tc.adopt {
				s.Put("roles", map[string]any{"oid": objRoleOID, "name": "Read-back role", "description": "original"})
			}
			fired := failReadBack(s, tc.after, "roles", objRoleOID, tc.status)
			cfg := objectConfig(fmt.Sprintf(`
  type           = "roles"
  oid            = %q
  adopt_existing = %t
  body = {
    name        = "Read-back role"
    description = "configured"
  }
`, objRoleOID, tc.adopt))
			steps := []resource.TestStep{
				{
					Config: cfg,
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(objAddr, "oid", objRoleOID),
						checkStub(func() error {
							if !fired() {
								return fmt.Errorf("the read-back fault did not fire")
							}
							if got := stubBody(s, "roles", objRoleOID)["description"]; got != "configured" {
								return fmt.Errorf("stored description = %v", got)
							}
							return nil
						}),
					),
				},
				{
					Config: cfg,
					ConfigPlanChecks: resource.ConfigPlanChecks{
						PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(objAddr, plancheck.ResourceActionNoop)},
					},
				},
			}
			if tc.adopt {
				// An adopted object is not destroyed; it leaves the state.
				steps = append(steps, removedStep(s, objAddr, "roles", objRoleOID))
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps:                    steps,
			})
		})
	}
}

// TestObject_updateReadBackFailureIsError: a failed read-back after an
// update stays an error (an update failure does not taint).
func TestObject_updateReadBackFailureIsError(t *testing.T) {
	s := newTestStub(t)
	cfg := func(desc string) string {
		return objectConfig(fmt.Sprintf(`
  type = "roles"
  oid  = %q
  body = { name = "Update role", description = %q }
`, objRoleOID, desc))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg("before")},
			{
				PreConfig:   func() { failReadBack(s, "PATCH", "roles", objRoleOID, 500) },
				Config:      cfg("after"),
				ExpectError: regexp.MustCompile(`Cannot read object`),
			},
			{
				PreConfig: func() { s.SetFault(nil) },
				Config:    cfg("after"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(objAddr, plancheck.ResourceActionNoop)},
				},
			},
		},
	})
}
