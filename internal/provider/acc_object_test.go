package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/normalize"
)

// endUserRoleOID is midPoint's built-in End user role.
const endUserRoleOID = "00000000-0000-0000-0000-000000000008"

// accClient is a client for the acceptance server from the MIDPOINT_*
// environment variables.
func accClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{
		URL:      os.Getenv("MIDPOINT_URL"),
		Username: os.Getenv("MIDPOINT_USERNAME"),
		Password: os.Getenv("MIDPOINT_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// accGet reads restType/oid from the acceptance server; nil when it does
// not exist.
func accGet(c *client.Client, restType, oid string) (map[string]any, error) {
	obj, err := c.Get(context.Background(), restType, oid, client.GetOptions{Exclude: readExcludes})
	if errors.Is(err, client.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return normalize.Canonical(restType, obj.Body), nil
}

// accCheckGone is a CheckDestroy that every midpoint_object in the state
// is gone from the server.
func accCheckGone(c *client.Client) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		for _, rs := range st.RootModule().Resources {
			if rs.Type != "midpoint_object" {
				continue
			}
			typ, oid := rs.Primary.Attributes["type"], rs.Primary.Attributes["oid"]
			body, err := accGet(c, typ, oid)
			if err != nil {
				return err
			}
			if body != nil {
				return fmt.Errorf("%s/%s still exists", typ, oid)
			}
		}
		return nil
	}
}

// accCheckItem checks one canonical top-level item of the object in the
// state at addr; want nil means absent.
func accCheckItem(c *client.Client, addr, item string, want any) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		rs, ok := st.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		body, err := accGet(c, rs.Primary.Attributes["type"], rs.Primary.Attributes["oid"])
		if err != nil {
			return err
		}
		if body == nil {
			return fmt.Errorf("%s does not exist in midPoint", addr)
		}
		if got := body[item]; !normalize.Equal(got, want) {
			return fmt.Errorf("%s %s = %v, want %v", addr, item, got, want)
		}
		return nil
	}
}

func importIDOf(addr string) resource.ImportStateIdFunc {
	return func(st *terraform.State) (string, error) {
		rs, ok := st.RootModule().Resources[addr]
		if !ok {
			return "", fmt.Errorf("%s not in state", addr)
		}
		return rs.Primary.Attributes["type"] + "/" + rs.Primary.Attributes["oid"], nil
	}
}

func TestAccObject_functionLibrary(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-lib")
	const addr = "midpoint_object.lib"
	cfg := func(body string) string {
		return fmt.Sprintf(`
resource "midpoint_object" "lib" {
  type = "functionLibraries"
  body = %s
}
`, body)
	}
	v1 := cfg(fmt.Sprintf(`{
    name        = %q
    description = "created by the acceptance test"
  }`, name))
	v2 := cfg(fmt.Sprintf(`{
    name        = %q
    description = "updated by the acceptance test"
    function = [{
      name       = "greet"
      returnType = "xsd:string"
      parameter  = [{ name = "who", type = "xsd:string" }]
      script     = [{ code = "'hello ' + who" }]
    }]
  }`, name))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accCheckGone(c),
		Steps: []resource.TestStep{
			{
				Config: v1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "oid"),
					resource.TestCheckResourceAttrPair(addr, "id", addr, "oid"),
					resource.TestCheckResourceAttr(addr, "name", name),
					accCheckItem(c, addr, "description", "created by the acceptance test"),
				),
			},
			{
				Config: v2,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					accCheckItem(c, addr, "description", "updated by the acceptance test"),
					func(st *terraform.State) error {
						rs := st.RootModule().Resources[addr]
						body, err := accGet(c, "functionLibraries", rs.Primary.Attributes["oid"])
						if err != nil {
							return err
						}
						if fns, _ := body["function"].([]any); len(fns) != 1 {
							return fmt.Errorf("want one function, got %v", body["function"])
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateIdFunc: importIDOf(addr),
				ImportStateVerify: true,
			},
			{
				// An import block with the same HCL plans no change.
				Config:            v2,
				ResourceName:      addr,
				ImportState:       true,
				ImportStateKind:   resource.ImportBlockWithID,
				ImportStateIdFunc: importIDOf(addr),
			},
		},
	})
}

func TestAccObject_detectsOutOfBandProperty(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-role")
	const addr = "midpoint_object.role"
	cfg := fmt.Sprintf(`
resource "midpoint_object" "role" {
  type = "roles"
  body = {
    name        = %q
    description = "managed by Terraform"
  }
}
`, name)
	var oid string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accCheckGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: func(st *terraform.State) error {
					oid = st.RootModule().Resources[addr].Primary.Attributes["oid"]
					return nil
				},
			},
			{
				PreConfig: func() {
					err := c.Modify(context.Background(), "roles", oid, []client.ItemDelta{
						{ModificationType: "add", Path: "riskLevel", Value: "high"},
					}, nil)
					if err != nil {
						t.Fatalf("out-of-band modify: %v", err)
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: accCheckItem(c, addr, "riskLevel", nil),
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

// TestAccObject_adoptExisting adopts the built-in End user role, changes
// its description and restores it. The role is never destroyed: every step
// that manages it sets prevent_destroy, and the last step drops it from
// state with a removed block (destroy = false). A cleanup restores the
// description if a step fails half-way.
func TestAccObject_adoptExisting(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c := accClient(t)
	const addr = "midpoint_object.end_user"

	actual, err := accGet(c, "roles", endUserRoleOID)
	if err != nil || actual == nil {
		t.Fatalf("reading the End user role: %v (found: %t)", err, actual != nil)
	}
	original, _ := actual["description"].(string)
	t.Cleanup(func() {
		now, err := accGet(c, "roles", endUserRoleOID)
		if err != nil || now == nil {
			t.Errorf("End user role after the test: %v (found: %t)", err, now != nil)
			return
		}
		if now["description"] == actual["description"] {
			return
		}
		var d client.ItemDelta
		if original == "" {
			d = client.ItemDelta{ModificationType: "replace", Path: "description"}
		} else {
			d = client.ItemDelta{ModificationType: "replace", Path: "description", Value: original}
		}
		if err := c.Modify(ctx, "roles", endUserRoleOID, []client.ItemDelta{d}, nil); err != nil {
			t.Errorf("restoring the End user role description: %v", err)
		}
	})

	// The adopted body is the whole role (body is authoritative), with the
	// description set to desc ("" leaves it out).
	bodyWith := func(desc string) map[string]any {
		b := stateBody(actual)
		delete(b, "description")
		if desc != "" {
			b["description"] = desc
		}
		return b
	}
	const custom = "End user role adopted by the midpoint_object acceptance test"

	// Guard the built-in role: adopting must change nothing but the
	// description.
	eng := newEngine(&providerData{Client: c}, objectSpec("roles"))
	desired := bodyWith(custom)
	desired["oid"] = endUserRoleOID
	raw, err := c.Get(ctx, "roles", endUserRoleOID, client.GetOptions{Exclude: readExcludes})
	if err != nil {
		t.Fatal(err)
	}
	_, deltas, err := eng.reconcile(ctx, desired, raw.Body)
	if err != nil || len(deltas) != 1 || deltas[0].Path != "description" {
		t.Fatalf("adopting the End user role would send %v (err %v); want only a description change", deltas, err)
	}

	cfg := func(desc string) string {
		js, err := json.Marshal(bodyWith(desc))
		if err != nil {
			t.Fatal(err)
		}
		heredoc := strings.NewReplacer("${", "$${", "%{", "%%{").Replace(string(js))
		return fmt.Sprintf(`
resource "midpoint_object" "end_user" {
  type           = "roles"
  oid            = %q
  adopt_existing = true
  body = jsondecode(<<-EOT
    %s
    EOT
  )

  lifecycle {
    prevent_destroy = true
  }
}
`, endUserRoleOID, heredoc)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(custom),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "oid", endUserRoleOID),
					resource.TestCheckResourceAttr(addr, "name", "End user"),
					accCheckItem(c, addr, "description", custom),
				),
			},
			{
				Config: cfg(original),
				Check:  accCheckItem(c, addr, "description", actual["description"]),
			},
			{
				Config: `
removed {
  from = midpoint_object.end_user

  lifecycle {
    destroy = false
  }
}
`,
				Check: func(*terraform.State) error {
					now, err := accGet(c, "roles", endUserRoleOID)
					if err != nil {
						return err
					}
					if now == nil {
						return fmt.Errorf("the End user role was deleted")
					}
					if !normalize.Equal(now, actual) {
						return fmt.Errorf("the End user role was not restored")
					}
					return nil
				},
			},
		},
	})
}
