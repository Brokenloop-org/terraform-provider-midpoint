package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
)

// accTriggerScannerOID is the built-in task that fires recompute triggers.
// It runs every 300 s; the test starts it so that it does not have to wait.
const accTriggerScannerOID = "00000000-0000-0000-0000-000000000007"

// accMemberRecomputeConfig is the setup of the member recompute guide: a
// metarole holding the policy rule, and an object of resourceType assigned to
// it, written without raw. A role written with the default options (raw)
// carries the same assignment as a control. With granted set, both induce the
// granted role.
func accMemberRecomputeConfig(name, resourceType string, granted bool) string {
	inducement := func(on bool) string {
		if !on {
			return ""
		}
		return `
  inducement {
    target {
      oid  = midpoint_role.granted.oid
      type = "RoleType"
    }
  }`
	}
	return fmt.Sprintf(`
resource "midpoint_role" "recompute_members" {
  name          = "%[1]s-metarole"
  force_destroy = true

  inducement {
    policy_rule = jsonencode({
      name = "recompute-members-on-inducement-change"
      policyConstraints = {
        modification = { item = "inducement" }
      }
      policyActions = {
        scriptExecution = {
          object = { linkSource = { description = "Members of the changed object" } }
          executeScript = {
            recompute = {
              triggered = {
                "@type"   = "c:TriggerCreationType"
                fireAfter = "PT1S"
              }
            }
          }
        }
      }
    })
  }
}

resource "midpoint_role" "granted" {
  name          = "%[1]s-granted"
  force_destroy = true
}

resource %[2]q "changed" {
  name          = "%[1]s-changed"
  options       = []
  force_destroy = true

  assignment {
    target {
      oid  = midpoint_role.recompute_members.oid
      type = "RoleType"
    }
  }
%[3]s
}

resource "midpoint_role" "raw_control" {
  name          = "%[1]s-raw"
  force_destroy = true

  assignment {
    target {
      oid  = midpoint_role.recompute_members.oid
      type = "RoleType"
    }
  }
%[3]s
}
`, name, resourceType, inducement(granted))
}

// accCreateMember creates a user assigned to holder through a normal (not
// raw) write, so that midPoint records the membership, and removes the user
// when the test ends.
func accCreateMember(t *testing.T, c *client.Client, name, holder, holderType string) string {
	t.Helper()
	oid, err := c.Create(context.Background(), client.Object{Type: "users", Body: map[string]any{
		"name": name,
		"assignment": []any{map[string]any{
			"targetRef": map[string]any{"oid": holder, "type": holderType},
		}},
	}}, nil)
	if err != nil {
		t.Fatalf("creating user %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := c.Delete(context.Background(), "users", oid, nil); err != nil {
			t.Errorf("cleanup: deleting user %s: %v", oid, err)
		}
	})
	return oid
}

// accMemberState returns whether user holds role in roleMembershipRef and how
// many triggers are waiting on the user.
func accMemberState(c *client.Client, user, role string) (holds bool, triggers int, err error) {
	obj, err := c.Get(context.Background(), "users", user, client.GetOptions{})
	if err != nil {
		return false, 0, err
	}
	for _, r := range asList(obj.Body["roleMembershipRef"]) {
		if m, _ := r.(map[string]any); m["oid"] == role {
			holds = true
		}
	}
	return holds, len(asList(obj.Body["trigger"])), nil
}

// accAwaitDelayedRecompute starts the trigger scanner until user holds role,
// and fails after 3 minutes.
func accAwaitDelayedRecompute(c *client.Client, user, role string) error {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		holds, _, err := accMemberState(c, user, role)
		if err != nil {
			return err
		}
		if holds {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("member %s does not hold the induced role %s after 3 minutes", user, role)
		}
		resp, body, err := accResourcePost("tasks/" + accTriggerScannerOID + "/run")
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("starting the trigger scanner: HTTP %d: %s", resp.StatusCode, body)
		}
		time.Sleep(10 * time.Second)
	}
}

// A change to the inducements of an object assigned to the metarole reaches
// its existing members, once the delayed recompute fires. The same change
// written raw (the default for roles) fires no rule: its member is left as it
// was. Breaks caught: a rule the provider cannot round-trip (the plan after
// the apply would not be empty), a rule midPoint does not fire for this object
// type, and raw writes starting to run policy rules, which would make the
// guide's warning wrong.
func TestAccMemberRecompute_onInducementChange(t *testing.T) {
	for _, tc := range []struct {
		resourceType, restType, midpointType string
	}{
		{"midpoint_role", "roles", "RoleType"},
		{"midpoint_archetype", "archetypes", "ArchetypeType"},
		{"midpoint_org", "orgs", "OrgType"},
	} {
		t.Run(tc.resourceType, func(t *testing.T) {
			testAccPreCheck(t)
			c := accClient(t)
			name := acctest.RandomWithPrefix("tf-acc-recompute")
			var changedOID, rawOID, grantedOID, member, rawMember string
			oidOf := func(st *terraform.State, addr string) string {
				return st.RootModule().Resources[addr].Primary.Attributes["oid"]
			}
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				CheckDestroy:             accFocusGone(c),
				Steps: []resource.TestStep{
					{
						Config: accMemberRecomputeConfig(name, tc.resourceType, false),
						Check: func(st *terraform.State) error {
							changedOID = oidOf(st, tc.resourceType+".changed")
							rawOID = oidOf(st, "midpoint_role.raw_control")
							grantedOID = oidOf(st, "midpoint_role.granted")
							return nil
						},
					},
					emptyPlanAfterRefresh(),
					{
						PreConfig: func() {
							member = accCreateMember(t, c, name+"-member", changedOID, tc.midpointType)
							rawMember = accCreateMember(t, c, name+"-raw-member", rawOID, "RoleType")
						},
						Config: accMemberRecomputeConfig(name, tc.resourceType, true),
						Check: func(*terraform.State) error {
							if err := accAwaitDelayedRecompute(c, member, grantedOID); err != nil {
								return err
							}
							holds, triggers, err := accMemberState(c, rawMember, grantedOID)
							if err != nil {
								return err
							}
							if holds || triggers != 0 {
								return fmt.Errorf("raw write reached its member: holds = %v, triggers = %d", holds, triggers)
							}
							return nil
						},
					},
					emptyPlanAfterRefresh(),
				},
			})
		})
	}
}

// A role first written raw, as every role is by default, can move to the setup
// in place: one apply turns raw off and assigns the metarole, and a later
// inducement change reaches the members. Break caught: the rule depending on
// something only a non-raw create records.
func TestAccMemberRecompute_existingRawRole(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-recompute-raw")
	full := func(granted bool) string { return accMemberRecomputeConfig(name, "midpoint_role", granted) }
	// The same objects before the move: the changed role is still raw and is
	// not assigned to the metarole.
	before := regexp.MustCompile(`(?s)(resource "midpoint_role" "changed" \{.*?)  options       = \[\]\n(.*?)  assignment \{.*?\n  \}\n`).
		ReplaceAllString(full(false), "$1$2")
	if before == full(false) {
		t.Fatal("the config before the move is the same as the config after it")
	}
	var changedOID, grantedOID, member string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: before,
				Check: func(st *terraform.State) error {
					changedOID = st.RootModule().Resources["midpoint_role.changed"].Primary.Attributes["oid"]
					grantedOID = st.RootModule().Resources["midpoint_role.granted"].Primary.Attributes["oid"]
					return nil
				},
			},
			{
				PreConfig: func() { member = accCreateMember(t, c, name+"-member", changedOID, "RoleType") },
				Config:    full(false),
			},
			{
				Config: full(true),
				Check: func(*terraform.State) error {
					return accAwaitDelayedRecompute(c, member, grantedOID)
				},
			},
			emptyPlanAfterRefresh(),
		},
	})
}

// The guide's one-off task brings existing members up to date after a change
// no rule reacted to: here a raw write, which leaves the member without the
// induced role until the task runs. Breaks caught: a member filter or activity
// midPoint does not run as written, and a task body the provider cannot
// round-trip.
func TestAccMemberRecompute_oneOffTask(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-recompute-task")
	cfg := func(task bool) string {
		out := fmt.Sprintf(`
resource "midpoint_role" "granted" {
  name          = "%[1]s-granted"
  force_destroy = true
}

resource "midpoint_role" "changed" {
  name          = "%[1]s-changed"
  force_destroy = true

  inducement {
    target {
      oid  = midpoint_role.granted.oid
      type = "RoleType"
    }
  }
}
`, name)
		if task {
			out += fmt.Sprintf(`
resource "midpoint_task" "recompute" {
  name                    = "%[1]s-task"
  initial_execution_state = "runnable"

  activity = {
    work = {
      recomputation = {
        objects = {
          type = "UserType"
          query = {
            filter = {
              text = "assignment matches (targetRef matches (oid = '${midpoint_role.changed.oid}'))"
            }
          }
        }
        executionOptions = { reconcile = false }
      }
    }
  }
}
`, name)
		}
		return out
	}
	var member, grantedOID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				// A raw create computes nothing, so the member starts in the
				// state a role change leaves behind: assigned, without the
				// induced role.
				Config: cfg(false),
				Check: func(st *terraform.State) error {
					grantedOID = st.RootModule().Resources["midpoint_role.granted"].Primary.Attributes["oid"]
					changed := st.RootModule().Resources["midpoint_role.changed"].Primary.Attributes["oid"]
					oid, err := c.Create(context.Background(), client.Object{Type: "users", Body: map[string]any{
						"name": name + "-member",
						"assignment": []any{map[string]any{
							"targetRef": map[string]any{"oid": changed, "type": "RoleType"},
						}},
					}}, []string{"raw"})
					if err != nil {
						return err
					}
					member = oid
					t.Cleanup(func() {
						if err := c.Delete(context.Background(), "users", oid, nil); err != nil {
							t.Errorf("cleanup: deleting user %s: %v", oid, err)
						}
					})
					holds, _, err := accMemberState(c, member, grantedOID)
					if err != nil {
						return err
					}
					if holds {
						return fmt.Errorf("member %s holds the induced role before the task ran", member)
					}
					return nil
				},
			},
			{
				Config: cfg(true),
				Check: func(*terraform.State) error {
					deadline := time.Now().Add(2 * time.Minute)
					for {
						holds, _, err := accMemberState(c, member, grantedOID)
						if err != nil {
							return err
						}
						if holds {
							return nil
						}
						if time.Now().After(deadline) {
							return fmt.Errorf("member %s does not hold the induced role %s 2 minutes after the task was created", member, grantedOID)
						}
						time.Sleep(5 * time.Second)
					}
				},
			},
			emptyPlanAfterRefresh(),
		},
	})
}
