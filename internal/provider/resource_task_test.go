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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const stubTaskOID = "07000000-0000-4000-8000-000000000003"

// taskConfig is a midpoint_task "t" recomputing nobody, with the given
// extra attribute lines.
func taskConfig(desc, attrs string) string {
	return fmt.Sprintf(`
resource "midpoint_task" "t" {
  oid         = %q
  name        = "recompute nobody"
  description = %q
  activity = {
    work = {
      recomputation = {
        objects = {
          type  = "UserType"
          query = { filter = { text = "name = \"nobody\"" } }
        }
      }
    }
  }
%s
}
`, stubTaskOID, desc, attrs)
}

var taskActivity = map[string]any{"work": map[string]any{"recomputation": map[string]any{"objects": map[string]any{
	"type": "UserType", "query": map[string]any{"filter": map[string]any{"text": `name = "nobody"`}},
}}}}

// serverFillsTask makes the stored task look like midPoint's after a create
// that left ownerRef and binding out: the caller as owner, binding loose.
func serverFillsTask(s interface {
	Put(string, map[string]any) string
}, body map[string]any) {
	body["ownerRef"] = map[string]any{"oid": accAdministratorOID, "type": "c:UserType"}
	body["binding"] = "loose"
	body["taskIdentifier"] = "1792672229603-28266-1"
	s.Put("tasks", body)
}

func TestTask_createSuspendedByDefault(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_task.t"
	var oid string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: taskConfig("v1", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("initial_execution_state"), knownvalue.StringExact("suspended")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("binding"), knownvalue.Null()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					checkStub(func() error {
						body, err := onePost(t, s, "/tasks", []string{"isImport"})
						if err != nil {
							return err
						}
						want := asJSON(t, map[string]any{
							"oid": stubTaskOID, "name": "recompute nobody", "description": "v1",
							"executionState": "suspended", "schedulingState": "suspended",
							"activity": taskActivity,
						})
						if !reflect.DeepEqual(body, want) {
							return fmt.Errorf("POST body = %v, want %v", body, want)
						}
						serverFillsTask(s, stubBody(s, "tasks", stubTaskOID))
						return nil
					}),
				),
			},
			// The server-set owner and binding are no drift.
			refreshIsClean(),
			{
				Config: taskConfig("v2", ""),
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
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
			// An import records the owner and binding midPoint set (see
			// TestTask_importRecordsOwnerAndBinding); with them unset in the
			// config the import is clean.
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateCheck:        checkImportedOwnerAndBinding,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"owner", "binding"},
			},
			{
				Config:          taskConfig("v2", ""),
				ResourceName:    addr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
			// initial_execution_state is create-only: changing it replaces
			// the task.
			{
				Config: taskConfig("v2", `  initial_execution_state = "runnable"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: checkStub(func() error {
					posts := requests(s, "POST")
					last := asJSON(t, posts[len(posts)-1].Body).(map[string]any)["task"].(map[string]any)
					if last["executionState"] != "runnable" || last["schedulingState"] != "ready" {
						return fmt.Errorf("re-created task states = %v/%v, want runnable/ready", last["executionState"], last["schedulingState"])
					}
					return nil
				}),
			},
		},
		CheckDestroy: checkGone(s, "tasks", &oid),
	})
}

// A runnable task suspended by hand is not resumed and shows no drift.
func TestTask_handSuspendIsNotDrift(t *testing.T) {
	s := newTestStub(t)
	cfg := func(desc string) string { return taskConfig(desc, `  initial_execution_state = "runnable"`) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg("v1"),
				Check: checkStub(func() error {
					body, err := onePost(t, s, "/tasks", []string{"isImport"})
					if err != nil {
						return err
					}
					if body["executionState"] != "runnable" || body["schedulingState"] != "ready" {
						return fmt.Errorf("POST states = %v/%v, want runnable/ready", body["executionState"], body["schedulingState"])
					}
					return nil
				}),
			},
			{
				PreConfig: func() {
					b := stubBody(s, "tasks", stubTaskOID)
					b["executionState"] = "suspended"
					b["schedulingState"] = "suspended"
					b["stateBeforeSuspend"] = "runnable"
					serverFillsTask(s, b)
				},
				Config:   cfg("v1"),
				PlanOnly: true,
			},
			{
				Config: cfg("v2"),
				Check: checkStub(func() error {
					stored := stubBody(s, "tasks", stubTaskOID)
					if stored["executionState"] != "suspended" || stored["description"] != "v2" {
						return fmt.Errorf("stored task = %v, want still suspended with description v2", stored)
					}
					for _, p := range requests(s, "PATCH") {
						for _, d := range asList(p.Body["objectModification"].(map[string]any)["itemDelta"]) {
							if path := d.(map[string]any)["path"]; path != "description" {
								return fmt.Errorf("PATCH touched %v", path)
							}
						}
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
	})
}

func TestTask_ownerBindingRecurrence(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_task.t"
	const otherOwner = "07000000-0000-4000-8000-0000000000ff"
	cfg := func(owner string) string {
		return taskConfig("v1", fmt.Sprintf(`
  owner      = %q
  binding    = "tight"
  recurrence = "recurring"
  schedule   = { interval = 3600 }
`, owner))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: cfg(accAdministratorOID),
				Check: checkStub(func() error {
					body, err := onePost(t, s, "/tasks", []string{"isImport"})
					if err != nil {
						return err
					}
					for k, want := range map[string]any{
						"ownerRef":   map[string]any{"oid": accAdministratorOID, "type": "UserType"},
						"binding":    "tight",
						"recurrence": "recurring",
						"schedule":   map[string]any{"interval": 3600},
					} {
						if got := body[k]; !reflect.DeepEqual(got, asJSON(t, want)) {
							return fmt.Errorf("POST %s = %v, want %v", k, got, want)
						}
					}
					return nil
				}),
			},
			refreshIsClean(),
			{
				Config: cfg(otherOwner),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner"), knownvalue.StringExact(otherOwner)),
				},
				Check: checkStub(func() error {
					ref, _ := stubBody(s, "tasks", stubTaskOID)["ownerRef"].(map[string]any)
					if ref["oid"] != otherOwner {
						return fmt.Errorf("stored ownerRef = %v, want %s", ref, otherOwner)
					}
					return nil
				}),
			},
			refreshIsClean(),
			{
				// The owner changed in midPoint shows as drift.
				PreConfig: func() {
					b := stubBody(s, "tasks", stubTaskOID)
					b["ownerRef"] = map[string]any{"oid": accAdministratorOID, "type": "c:UserType"}
					s.Put("tasks", b)
				},
				Config:             cfg(otherOwner),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestTask_validation(t *testing.T) {
	s := newTestStub(t)
	for _, tc := range []struct{ attrs, want string }{
		{`  initial_execution_state = "running"`, `value must be one of`},
		{`  extra = { ownerRef = { oid = "x" } }`, `ownerRef is a typed attribute`},
		{`  extra = { executionState = "runnable" }`, `executionState is a typed attribute`},
		{`  extra = { activity = {} }`, `activity is a typed attribute`},
	} {
		t.Run(tc.attrs, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{{
					Config:      taskConfig("v1", tc.attrs),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

// An imported task records initial_execution_state as suspended; a
// configured runnable is then a no-op update, never a re-creation.
func TestTask_importedIsNeverReplaced(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_task.t"
	body := map[string]any{
		"oid": stubTaskOID, "name": "recompute nobody", "description": "v1", "activity": taskActivity,
		"executionState": "runnable", "schedulingState": "ready",
	}
	serverFillsTask(s, body)
	cfg := taskConfig("v1", `  initial_execution_state = "runnable"`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config:             cfg,
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      stubTaskOID,
				ImportStatePersist: true,
				ImportStateCheck: func(st []*terraform.InstanceState) error {
					if got := st[0].Attributes["initial_execution_state"]; got != "suspended" {
						return fmt.Errorf("imported initial_execution_state = %q, want suspended", got)
					}
					return nil
				},
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					for _, m := range []string{"POST", "DELETE", "PATCH"} {
						if n := len(requests(s, m)); n != 0 {
							return fmt.Errorf("%d %s requests, want none", n, m)
						}
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
	})
}

// checkImportedOwnerAndBinding checks that an imported task recorded the
// owner and binding serverFillsTask set.
func checkImportedOwnerAndBinding(st []*terraform.InstanceState) error {
	a := st[0].Attributes
	if a["owner"] != accAdministratorOID || a["binding"] != "loose" {
		return fmt.Errorf("imported owner/binding = %q/%q, want %s/loose", a["owner"], a["binding"], accAdministratorOID)
	}
	return nil
}

// An import records the owner and binding the server holds. A config that
// sets them as midPoint holds them (the converter's) imports with no
// change, and so does one that leaves them unset: the recorded values stay
// in state, unsent and uncompared. Configuring them later is compared as
// usual, and removing them again plans null.
func TestTask_importRecordsOwnerAndBinding(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_task.t"
	const otherOwner = "0a000000-0000-4000-8000-0000000000aa"
	body := map[string]any{"oid": stubTaskOID, "name": "recompute nobody", "description": "v1", "activity": taskActivity,
		"executionState": "suspended", "schedulingState": "suspended"}
	serverFillsTask(s, body)
	withOwner := func(owner string) string {
		return taskConfig("v1", fmt.Sprintf("  owner   = %q\n  binding = \"loose\"", owner))
	}
	unset := taskConfig("v1", "")
	noWrites := checkStub(func() error {
		for _, m := range []string{"POST", "DELETE", "PATCH"} {
			if n := len(requests(s, m)); n != 0 {
				return fmt.Errorf("%d %s requests, want none", n, m)
			}
		}
		return nil
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config:             unset,
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      stubTaskOID,
				ImportStatePersist: true,
				ImportStateCheck:   checkImportedOwnerAndBinding,
			},
			// (1) set as midPoint holds them: a clean import block
			{
				Config:          withOwner(accAdministratorOID),
				ResourceName:    addr,
				ImportState:     true,
				ImportStateId:   stubTaskOID,
				ImportStateKind: resource.ImportBlockWithID,
			},
			// (2) unset: a clean import block too
			{
				Config:          unset,
				ResourceName:    addr,
				ImportState:     true,
				ImportStateId:   stubTaskOID,
				ImportStateKind: resource.ImportBlockWithID,
			},
			{
				Config: unset,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner"), knownvalue.StringExact(accAdministratorOID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("binding"), knownvalue.StringExact("loose")),
				},
				Check: noWrites,
			},
			// A change in midPoint to an unset owner is followed, never a
			// difference.
			{
				PreConfig: func() {
					b := stubBody(s, "tasks", stubTaskOID)
					b["ownerRef"] = map[string]any{"oid": otherOwner, "type": "c:UserType"}
					s.Put("tasks", b)
				},
				Config:   unset,
				PlanOnly: true,
			},
			{
				Config: unset,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner"), knownvalue.StringExact(otherOwner)),
				},
				Check: noWrites,
			},
			// (3) configuring a different owner is an update that sends it
			{
				Config: withOwner(accAdministratorOID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					patches := requests(s, "PATCH")
					if len(patches) != 1 {
						return fmt.Errorf("want one PATCH, got %d", len(patches))
					}
					if got := fmt.Sprint(asJSON(t, patches[0].Body)); !strings.Contains(got, "ownerRef") || strings.Contains(got, "binding") {
						return fmt.Errorf("PATCH body = %s, want the ownerRef alone", got)
					}
					return nil
				}),
			},
			refreshIsClean(),
			// Configured, then removed: null again, nothing sent.
			{
				Config: unset,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("binding"), knownvalue.Null()),
				},
				Check: checkStub(func() error {
					if n := len(requests(s, "PATCH")); n != 1 {
						return fmt.Errorf("%d PATCH requests in all, want the one of the owner change", n)
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
	})
}

// After a run midPoint has assigned the task its archetype; with no
// assignment configured that is no drift and never deleted.
func TestTask_runArchetypeAssignmentIsNotDrift(t *testing.T) {
	s := newTestStub(t)
	cfg := func(desc string) string { return taskConfig(desc, "") }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfg("v1")},
			{
				PreConfig: func() {
					b := stubBody(s, "tasks", stubTaskOID)
					b["assignment"] = []any{archetypeAssignment("00000000-0000-0000-0000-000000000502")}
					serverFillsTask(s, b)
				},
				Config:   cfg("v1"),
				PlanOnly: true,
			},
			refreshIsClean(),
			{
				Config: cfg("v2"),
				Check: checkStub(func() error {
					for _, p := range requests(s, "PATCH") {
						for _, d := range asList(p.Body["objectModification"].(map[string]any)["itemDelta"]) {
							if path := d.(map[string]any)["path"]; path != "description" {
								return fmt.Errorf("PATCH touched %v", path)
							}
						}
					}
					if a := asList(stubBody(s, "tasks", stubTaskOID)["assignment"]); len(a) != 1 {
						return fmt.Errorf("stored assignments = %v, want the archetype one kept", a)
					}
					return nil
				}),
			},
			refreshIsClean(),
		},
	})
}

// An adopted task was not created by the provider: removing
// initial_execution_state (a change back to the default) is a no-op
// update, never a re-creation.
func TestTask_adoptedIsNeverReplaced(t *testing.T) {
	s := newTestStub(t)
	const addr = "midpoint_task.t"
	serverFillsTask(s, map[string]any{
		"oid": stubTaskOID, "name": "recompute nobody", "description": "v1", "activity": taskActivity,
		"executionState": "runnable", "schedulingState": "ready",
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: taskConfig("v1", "  adopt_existing = true\n  initial_execution_state = \"runnable\""),
				Check: checkStub(func() error {
					if n := len(requests(s, "POST")); n != 0 {
						return fmt.Errorf("%d POST requests, want none (adopted)", n)
					}
					return nil
				}),
			},
			{
				Config: taskConfig("v1", "  adopt_existing = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkStub(func() error {
					for _, m := range []string{"POST", "DELETE"} {
						if n := len(requests(s, m)); n != 0 {
							return fmt.Errorf("%d %s requests, want none", n, m)
						}
					}
					return nil
				}),
			},
			refreshIsClean(),
			{
				Config:      taskConfig("v1", "  adopt_existing = true"),
				Destroy:     true,
				ExpectError: adoptedDeleteRefused("tasks", stubTaskOID),
			},
			removedStep(s, addr, "tasks", stubTaskOID),
		},
	})
}
