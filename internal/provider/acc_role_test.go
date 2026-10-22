package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

// accRawVersion returns the version of restType/oid as a number.
func accRawVersion(c *client.Client, restType, oid string) (int, error) {
	obj, err := c.Get(context.Background(), restType, oid, client.GetOptions{Exclude: readExcludes})
	if err != nil {
		return 0, err
	}
	v, _ := obj.Body["version"].(string)
	return strconv.Atoi(v)
}

// accConstructionInducement returns the inducement of a canonical role body
// that holds a construction.
func accConstructionInducement(body map[string]any) (map[string]any, error) {
	for _, v := range asList(body["inducement"]) {
		if m, _ := v.(map[string]any); m["construction"] != nil {
			return m, nil
		}
	}
	return nil, fmt.Errorf("no construction inducement in %v", body["inducement"])
}

// TestAccRole_construction applies a role that induces an account on the
// CSV resource (a construction with kind, intent, order and a body) next
// to a policy rule. The plan is clean after the apply. Changing the
// construction body changes that item in place: the role read before and
// after differs in nothing else, every container keeps its id (no
// delete and add of the inducement), and the role was written once.
func TestAccRole_construction(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	fixtures := accCSVFixtures(t, c)
	name := acctest.RandomWithPrefix("tf-acc-cons")
	const addr = "midpoint_role.r"
	cfg := func(description string) string {
		return accCSVResourceConfig(fixtures, name+" / csv", "schema-handling.yaml", "") + fmt.Sprintf(`
resource "midpoint_role" "r" {
  name = %[1]q

  inducement {
    order = 2
    construction {
      resource = midpoint_resource.csv.oid
      kind     = "account"
      intent   = "default"
      body = jsonencode({
        description = %[2]q
        attribute = [{
          ref      = "ri:fullName"
          outbound = [{ strength = "strong", source = [{ path = "fullName" }] }]
        }]
      })
    }
  }

  inducement {
    policy_rule = jsonencode({
      name = "no-end-user"
      policyConstraints = {
        exclusion = { targetRef = { oid = %[3]q, type = "RoleType" } }
      }
    })
  }
}
`, name+" / role", description, endUserRoleOID)
	}
	var oid string
	var before map[string]any
	var beforeVersion int
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             resource.ComposeAggregateTestCheckFunc(accFocusGone(c), accResourceGone(c)),
		Steps: []resource.TestStep{
			{
				Config: cfg("v1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureOID(addr, &oid),
					func(st *terraform.State) error {
						body, err := accGet(c, "roles", oid)
						if err != nil {
							return err
						}
						ind, err := accConstructionInducement(body)
						if err != nil {
							return err
						}
						cons, _ := ind["construction"].(map[string]any)
						res, _ := cons["resourceRef"].(map[string]any)
						csvOID, err := accResourceOID(st, "midpoint_resource.csv")
						if err != nil {
							return err
						}
						if res["oid"] != csvOID || cons["kind"] != "account" || cons["intent"] != "default" ||
							cons["description"] != "v1" || fmt.Sprint(ind["order"]) != "2" {
							return fmt.Errorf("stored construction inducement = %v", ind)
						}
						if n := len(asList(body["inducement"])); n != 2 {
							return fmt.Errorf("role has %d inducements, want 2", n)
						}
						return nil
					},
				),
			},
			emptyPlanAfterRefresh(),
			{Config: cfg("v1"), PlanOnly: true},
			{
				PreConfig: func() {
					var err error
					if before, err = accGet(c, "roles", oid); err != nil {
						t.Fatal(err)
					}
					if beforeVersion, err = accRawVersion(c, "roles", oid); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg("v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: func(*terraform.State) error {
					after, err := accGet(c, "roles", oid)
					if err != nil {
						return err
					}
					want := deepCopyJSON(t, before)
					ind, err := accConstructionInducement(want)
					if err != nil {
						return err
					}
					ind["construction"].(map[string]any)["description"] = "v2"
					if !reflect.DeepEqual(after, want) {
						a, _ := json.Marshal(after)
						w, _ := json.Marshal(want)
						return fmt.Errorf("role after the body change (container ids included) =\n%s\nwant\n%s", a, w)
					}
					version, err := accRawVersion(c, "roles", oid)
					if err != nil {
						return err
					}
					if version != beforeVersion+1 {
						return fmt.Errorf("version %d -> %d, want one write", beforeVersion, version)
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
		},
	})
}

// deepCopyJSON copies a decoded JSON value.
func deepCopyJSON(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := parseJSON(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

// TestAccRole_addFilterInducementByUpdate creates a role without
// inducements and adds a filter-only inducement by an update (a PATCH that
// adds a reference with a filter only, sent with reevaluateSearchFilters).
// midPoint resolves the filter; the plan is clean afterwards.
func TestAccRole_addFilterInducementByUpdate(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-addflt")
	cfg := func(inducement string) string {
		return fmt.Sprintf(`
resource "midpoint_role" "target" {
  name = "%[1]s / target"
}

resource "midpoint_role" "business" {
  name       = "%[1]s / business"
  depends_on = [midpoint_role.target]
%[2]s}
`, name, inducement)
	}
	const inducement = `
  inducement {
    target {
      type   = "RoleType"
      filter = "name = \"${midpoint_role.target.name}\""
    }
  }
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check:  accFocusItem(c, "midpoint_role.business", "inducement", nil),
			},
			{
				Config: cfg(inducement),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("midpoint_role.business", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("midpoint_role.business", "inducement.0.target.oid", "midpoint_role.target", "oid"),
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
			{Config: cfg(inducement), PlanOnly: true},
		},
	})
}

// TestAccRole_extension writes an extension item that no midPoint schema
// extension defines (the acceptance environment has none). midPoint 4.9
// rejects it on a write that is not raw, and the provider reports that
// error and creates nothing. A raw write (the role default) stores the item
// without a definition, and midPoint returns it as "#costCenter", which is
// not the configured item: the plan never converges. extension therefore
// requires a matching schema extension in midPoint (README).
func TestAccRole_extension(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-ext")
	const addr = "midpoint_role.r"
	cfg := func(options string) string {
		return fmt.Sprintf(`
resource "midpoint_role" "r" {
  name      = %q
  extension = { costCenter = "CC-1" }
%s}
`, name, options)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accFocusGone(c),
		Steps: []resource.TestStep{
			{
				Config:      cfg(`  options = ["isImport"]` + "\n"),
				ExpectError: regexp.MustCompile(`(?s)Cannot create object.*No definition in PP\(costCenter\)`),
			},
			{
				PreConfig: func() {
					objs, _, err := c.Search(context.Background(), "roles", fmt.Sprintf("name = %q", name), 1, nil)
					if err != nil || len(objs) != 0 {
						t.Fatalf("the rejected create left %d roles named %s (err %v)", len(objs), name, err)
					}
				},
				Config: cfg(""),
				// The item midPoint returns is not the configured one.
				ExpectNonEmptyPlan: true,
				Check: func(st *terraform.State) error {
					oid, err := stateOID(st, addr)
					if err != nil {
						return err
					}
					obj, err := c.Get(context.Background(), "roles", oid, client.GetOptions{Exclude: readExcludes})
					if err != nil {
						return err
					}
					if ext, _ := obj.Body["extension"].(map[string]any); ext["#costCenter"] != "CC-1" {
						return fmt.Errorf("stored extension = %v, want the undefined item #costCenter", obj.Body["extension"])
					}
					return nil
				},
			},
			{Config: cfg(""), PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}
