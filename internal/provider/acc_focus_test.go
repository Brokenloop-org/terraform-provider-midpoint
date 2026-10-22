package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

const (
	// Built-in archetypes: Application (for services) and Application role.
	accApplicationArchetype     = "00000000-0000-0000-0000-000000000329"
	accApplicationRoleArchetype = "00000000-0000-0000-0000-000000000328"
	accAdministratorOID         = "00000000-0000-0000-0000-000000000002"
)

// focusRestTypes maps the role-like resource types to REST types.
var focusRestTypes = map[string]string{
	"midpoint_service":   "services",
	"midpoint_role":      "roles",
	"midpoint_org":       "orgs",
	"midpoint_archetype": "archetypes",
}

// accFocusGone is a CheckDestroy that every role-like object in the state
// is gone from the server.
func accFocusGone(c *client.Client) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		for _, rs := range st.RootModule().Resources {
			typ, ok := focusRestTypes[rs.Type]
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

// accFocusItem checks one canonical top-level item of the role-like object
// at addr; want nil means absent.
func accFocusItem(c *client.Client, addr, item string, want any) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		rs, ok := st.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		body, err := accGet(c, focusRestTypes[rs.Type], rs.Primary.Attributes["oid"])
		if err != nil {
			return err
		}
		if body == nil {
			return fmt.Errorf("%s does not exist in midPoint", addr)
		}
		got := stripIDs(body[item])
		if !normalize.Equal(got, want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(want)
			return fmt.Errorf("%s %s = %s, want %s", addr, item, gj, wj)
		}
		return nil
	}
}

// accRawArchetypeRef checks that the object at addr, created raw, holds the
// archetypeRef the provider writes with the archetype assignment (raw
// writes skip the recompute that would set it).
func accRawArchetypeRef(c *client.Client, addr, archetype string) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		rs, ok := st.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		obj, err := c.Get(context.Background(), focusRestTypes[rs.Type], rs.Primary.Attributes["oid"], client.GetOptions{})
		if err != nil {
			return err
		}
		for _, r := range asList(obj.Body["archetypeRef"]) {
			if m, _ := r.(map[string]any); m["oid"] == archetype {
				return nil
			}
		}
		return fmt.Errorf("%s has no archetypeRef to %s: %v", addr, archetype, obj.Body["archetypeRef"])
	}
}

func emptyPlanAfterRefresh() resource.TestStep {
	return resource.TestStep{
		RefreshState: true,
		RefreshPlanChecks: resource.RefreshPlanChecks{
			PostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
		},
	}
}

func TestAccService_withRoles(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-svc")
	cfg := func(adminDesc string) string {
		return fmt.Sprintf(`
data "midpoint_object" "application" {
  type = "archetypes"
  name = "Application"
}

resource "midpoint_service" "app" {
  name         = %[1]q
  display_name = "Acceptance service"
  archetype    = data.midpoint_object.application.oid
}

resource "midpoint_role" "admin" {
  name         = "%[1]s / admin"
  display_name = "Admin"
  identifier   = "admin"
  description  = %[2]q
  archetype    = %[3]q

  inducement {
    target {
      oid  = midpoint_service.app.oid
      type = "ServiceType"
    }
  }
}

resource "midpoint_role" "user" {
  name       = "%[1]s / user"
  identifier = "user"
  archetype  = %[3]q

  inducement {
    target {
      oid  = midpoint_service.app.oid
      type = "ServiceType"
    }
  }
}
`, name, adminDesc, accApplicationRoleArchetype)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg("first"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("midpoint_service.app", tfjsonpath.New("archetype"), knownvalue.StringExact(accApplicationArchetype)),
					statecheck.ExpectKnownValue("midpoint_service.app", tfjsonpath.New("extra"), knownvalue.Null()),
					statecheck.ExpectKnownValue("midpoint_role.admin", tfjsonpath.New("assignment"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue("midpoint_role.admin", tfjsonpath.New("inducement"), knownvalue.ListSizeExact(1)),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("midpoint_role.admin", "inducement.0.target.oid", "midpoint_service.app", "oid"),
					accFocusItem(c, "midpoint_role.admin", "description", "first"),
					accFocusItem(c, "midpoint_service.app", "assignment", []any{map[string]any{"targetRef": map[string]any{
						"oid": accApplicationArchetype, "type": "ArchetypeType", "relation": "org:default",
					}}}),
					accRawArchetypeRef(c, "midpoint_role.admin", accApplicationRoleArchetype),
				),
			},
			emptyPlanAfterRefresh(),
			{
				Config: cfg("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("midpoint_role.admin", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("midpoint_role.user", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction("midpoint_service.app", plancheck.ResourceActionNoop),
					},
				},
				Check: accFocusItem(c, "midpoint_role.admin", "description", "second"),
			},
			emptyPlanAfterRefresh(),
			{ResourceName: "midpoint_service.app", ImportState: true, ImportStateVerify: true},
			{ResourceName: "midpoint_role.admin", ImportState: true, ImportStateVerify: true},
			{
				Config:          cfg("second"),
				ResourceName:    "midpoint_role.admin",
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
		},
	})
}

// roleWithServiceConfig is a service and a role inducing it, plus extra
// HCL inside the role.
func roleWithServiceConfig(name, roleExtra string) string {
	return fmt.Sprintf(`
resource "midpoint_service" "app" {
  name = %[1]q
}

resource "midpoint_role" "r" {
  name = "%[1]s / access"

  inducement {
    target {
      oid  = midpoint_service.app.oid
      type = "ServiceType"
    }
  }
%[2]s
}
`, name, roleExtra)
}

func TestAccRole_singleInducement(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-ind")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				// focusType is a QName: midPoint stores "#UserType".
				Config: roleWithServiceConfig(name, `
  inducement {
    target {
      oid  = "`+endUserRoleOID+`"
      type = "RoleType"
    }
    extra = jsonencode({ focusType = "UserType", description = "for users only" })
  }
`),
			},
			emptyPlanAfterRefresh(),
		},
	})
}

func TestAccRole_filterInducement(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-flt")
	cfg := fmt.Sprintf(`
resource "midpoint_role" "target" {
  name = "%[1]s / target"
}

resource "midpoint_role" "business" {
  name = "%[1]s / business"

  inducement {
    target {
      type   = "RoleType"
      filter = "name = \"${midpoint_role.target.name}\""
    }
  }
}
`, name)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("midpoint_role.business", "inducement.0.target.oid"),
					// midPoint resolved the filter to the target role.
					func(st *terraform.State) error {
						target, err := stateOID(st, "midpoint_role.target")
						if err != nil {
							return err
						}
						return accFocusItem(c, "midpoint_role.business", "inducement", []any{map[string]any{"targetRef": map[string]any{
							"oid": target, "type": "RoleType", "relation": "org:default",
							"filter": map[string]any{"text": fmt.Sprintf("name = %q", name+" / target")},
						}}})(st)
					},
				),
			},
			emptyPlanAfterRefresh(),
			{Config: cfg, PlanOnly: true},
		},
	})
}

func TestAccRole_detectsOutOfBandInducement(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-oob")
	cfg := roleWithServiceConfig(name, "")
	var roleOID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: func(st *terraform.State) error {
					var err error
					roleOID, err = stateOID(st, "midpoint_role.r")
					return err
				},
			},
			{
				// A GUI user induces the built-in End user role.
				PreConfig: func() {
					err := c.Modify(context.Background(), "roles", roleOID, []client.ItemDelta{{
						ModificationType: "add", Path: "inducement",
						Value: []any{map[string]any{"targetRef": map[string]any{"oid": endUserRoleOID, "type": "RoleType"}}},
					}}, nil)
					if err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("midpoint_role.r", plancheck.ResourceActionUpdate)},
				},
				Check: func(st *terraform.State) error {
					body, err := accGet(c, "roles", roleOID)
					if err != nil {
						return err
					}
					if n := len(asList(body["inducement"])); n != 1 {
						return fmt.Errorf("role has %d inducements after the apply, want 1", n)
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
		},
	})
}

// accAssignmentID returns the container id of the assignment of user that
// targets oid; 0 when there is none.
func accAssignmentID(c *client.Client, user, oid string) (json.Number, error) {
	body, err := accGet(c, "users", user)
	if err != nil || body == nil {
		return "", err
	}
	for _, a := range asList(body["assignment"]) {
		am, _ := a.(map[string]any)
		ref, _ := am["targetRef"].(map[string]any)
		if ref["oid"] == oid {
			id, _ := am["@id"].(json.Number)
			return id, nil
		}
	}
	return "", nil
}

// accUnassign removes the assignment of user to oid, if any.
func accUnassign(c *client.Client, user, oid string) error {
	id, err := accAssignmentID(c, user, oid)
	if err != nil || id == "" {
		return err
	}
	return c.Modify(context.Background(), "users", user, []client.ItemDelta{{
		ModificationType: "delete", Path: "assignment", Value: []any{map[string]any{"@id": id}},
	}}, nil)
}

func TestAccRole_forceDestroyRequiredWithMember(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-mbr")
	cfg := fmt.Sprintf(`
resource "midpoint_role" "r" {
  name = %q
}
`, name)
	var roleOID string
	t.Cleanup(func() {
		// Never leave the administrator with an assignment to a test role.
		if roleOID == "" {
			return
		}
		if err := accUnassign(c, accAdministratorOID, roleOID); err != nil {
			t.Errorf("cleanup: removing the administrator assignment: %v", err)
		}
		if body, err := accGet(c, "roles", roleOID); err == nil && body != nil {
			if err := c.Delete(context.Background(), "roles", roleOID, nil); err != nil {
				t.Errorf("cleanup: deleting role %s: %v", roleOID, err)
			}
		}
	})
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: func(st *terraform.State) error {
					var err error
					roleOID, err = stateOID(st, "midpoint_role.r")
					return err
				},
			},
			{
				PreConfig: func() {
					err := c.Modify(context.Background(), "users", accAdministratorOID, []client.ItemDelta{{
						ModificationType: "add", Path: "assignment",
						Value: []any{map[string]any{"targetRef": map[string]any{"oid": roleOID, "type": "RoleType"}}},
					}}, nil)
					if err != nil {
						t.Fatal(err)
					}
				},
				Config:      cfg,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`still\s+reference\s+it`),
			},
			{
				PreConfig: func() {
					if err := accUnassign(c, accAdministratorOID, roleOID); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg,
			},
		},
	})
}

func TestAccOrg_tenant(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-tenant")
	const addr = "midpoint_org.tenant"
	cfg := func(desc string) string {
		return fmt.Sprintf(`
resource "midpoint_org" "tenant" {
  name         = %q
  display_name = "Acceptance tenant"
  description  = %q
  tenant       = true
}
`, name, desc)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg("first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "tenant", "true"),
					accFocusItem(c, addr, "tenant", true),
				),
			},
			emptyPlanAfterRefresh(),
			{Config: cfg("second"), Check: accFocusItem(c, addr, "description", "second")},
			emptyPlanAfterRefresh(),
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		},
	})
}

func TestAccArchetype_withPolicy(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-arch")
	const addr = "midpoint_archetype.cluster"
	cfg := func(label string) string {
		return fmt.Sprintf(`
resource "midpoint_archetype" "cluster" {
  name        = %q
  holder_type = ["RoleType"]

  authorization = [{
    name   = "read-clusters"
    action = ["http://midpoint.evolveum.com/xml/ns/public/security/authorization-model-3#read"]
  }]

  archetype_policy = {
    display = {
      label       = %q
      pluralLabel = "Clusters"
      icon        = { cssClass = "fa fa-cubes" }
    }
  }
}
`, name, label)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg("Cluster"),
				Check: resource.ComposeAggregateTestCheckFunc(
					accFocusItem(c, addr, "assignment", []any{map[string]any{
						"identifier":         "holderType",
						"assignmentRelation": []any{map[string]any{"holderType": []any{"RoleType"}}},
					}}),
					accFocusItem(c, addr, "authorization", []any{map[string]any{
						"name":   "read-clusters",
						"action": []any{"http://midpoint.evolveum.com/xml/ns/public/security/authorization-model-3#read"},
					}}),
				),
			},
			emptyPlanAfterRefresh(),
			{
				Config: cfg("K8s cluster"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
			},
			emptyPlanAfterRefresh(),
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		},
	})
}
