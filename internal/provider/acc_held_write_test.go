package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// administratorOID is midPoint's built-in administrator.
const administratorOID = "00000000-0000-0000-0000-000000000002"

// TestAccHeldUpdate_strictFails puts an org under an approval rule for
// modifications (not creates or deletes, so the test can set up and clean up)
// and checks that fail_on_unapplied_write fails the held update. midPoint
// answers that update with a plain 204.
func TestAccHeldUpdate_strictFails(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-held")
	config := func(description string) string {
		return fmt.Sprintf(`
provider "midpoint" {
  fail_on_unapplied_write = true
}

resource "midpoint_object" "org" {
  type = "orgs"
  body = {
    name        = %q
    description = %q
    assignment = [{
      policyRule = {
        name              = "approve-modifications"
        policyConstraints = { modification = { operation = ["modify"] } }
        policyActions = {
          approval = { approverRef = { oid = %q, type = "UserType" } }
        }
      }
    }]
  }
}
`, name, description, administratorOID)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accCheckGone(c),
		Steps: []resource.TestStep{
			{Config: config("before")},
			{
				Config:      config("after"),
				ExpectError: regexp.MustCompile(`Write not reflected in midPoint`),
			},
			// midPoint still holds the old value, so the old configuration
			// plans nothing.
			{Config: config("before"), PlanOnly: true},
		},
	})
}
