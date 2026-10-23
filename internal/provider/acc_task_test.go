package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

// accTaskSuspended checks, on the raw server body, that the task at addr is
// suspended: it was never started.
func accTaskSuspended(c *client.Client, addr string) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		oid := st.RootModule().Resources[addr].Primary.Attributes["oid"]
		obj, err := c.Get(context.Background(), "tasks", oid, client.GetOptions{})
		if err != nil {
			return err
		}
		if obj.Body["executionState"] != "suspended" {
			return fmt.Errorf("task %s executionState = %v, want suspended", oid, obj.Body["executionState"])
		}
		for _, item := range []string{"lastRunStartTimestamp", "progress"} {
			if v := obj.Body[item]; v != nil {
				return fmt.Errorf("task %s has %s = %v; it ran", oid, item, v)
			}
		}
		return nil
	}
}

// The recompute task of the Task 3 fixture, created suspended: it never
// runs against the live server.
func TestAccTask_recomputeSuspended(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-task")
	cfg := func(desc string, attrs ...string) string {
		return fmt.Sprintf(`
resource "midpoint_task" "recompute" {
  name                    = %q
  description             = %q
  initial_execution_state = "suspended"
%s
  activity = {
    work = {
      recomputation = {
        objects = {
          type  = "UserType"
          query = { filter = { text = "name = \"tf-acc-nobody\"" } }
        }
      }
    }
  }
}
`, name, desc, strings.Join(attrs, "\n"))
	}
	const addr = "midpoint_task.recompute"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accSimpleGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg("first"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("binding"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("extra"), knownvalue.Null()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					accTaskSuspended(c, addr),
					accSimpleItem(c, addr, "ownerRef", map[string]any{"oid": accAdministratorOID, "type": "UserType"}),
				),
			},
			emptyPlanAfterRefresh(),
			{
				Config: cfg("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					accSimpleItem(c, addr, "description", "second"),
					accTaskSuspended(c, addr),
				),
			},
			emptyPlanAfterRefresh(),
			// An import records the owner (the caller) and binding midPoint
			// set; a config that sets them as midPoint holds them imports
			// with no change.
			{ResourceName: addr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"owner", "binding"}},
			{
				Config:          cfg("second", fmt.Sprintf("  owner = %q", accAdministratorOID), `  binding = "loose"`),
				ResourceName:    addr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
		},
	})
}
