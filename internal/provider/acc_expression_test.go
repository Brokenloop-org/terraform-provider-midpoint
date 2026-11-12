package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// midPoint 4.9 reads the evaluators of an expression as lists however they
// were written, and the filter inside a search evaluator too. Each form below
// must settle: no plan after the apply.
func TestAccObjectTemplate_expressionEvaluatorForms(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	tests := []struct{ name, target, expression string }{
		{"search_bare", "assignment", `
      assignmentTargetSearch:
        targetType: RoleType
        filter:
          text: name = "x"`},
		{"search_structured_filter_bare", "assignment", `
      assignmentTargetSearch:
        targetType: RoleType
        filter:
          equal:
            path: name
            value: "y"`},
		{"search_list", "assignment", `
      assignmentTargetSearch:
        - targetType: RoleType
          filter:
            - text: name = "x"`},
		{"search_list_filter_bare", "assignment", `
      assignmentTargetSearch:
        - targetType: RoleType
          filter:
            text: name = "x"`},
		{"reference_search_bare", "assignment", `
      referenceSearch:
        targetType: RoleType
        filter:
          text: name = "x"`},
		{"association_from_link_bare", "assignment", `
      associationFromLink:
        projectionDiscriminator:
          kind: account
          intent: default`},
		{"association_from_link_list", "assignment", `
      associationFromLink:
        - projectionDiscriminator:
            kind: account
            intent: default`},
		{"generate_bare", "description", `
      generate:
        mode: uuid`},
		{"as_is_bare", "description", `
      asIs: {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := acctest.RandomWithPrefix("tf-acc-expr")
			cfg := fmt.Sprintf(`
resource "midpoint_object_template" "t" {
  name = %q
  mapping = [yamldecode(<<-EOT
    name: m
    target:
      path: %s
    expression:%s
  EOT
  )]
}
`, name, tt.target, tt.expression)
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				CheckDestroy:             accSimpleGone(c),
				Steps: []resource.TestStep{
					{Config: cfg},
					emptyPlanAfterRefresh(),
				},
			})
		})
	}
}

// An edit of a bare evaluator replaces the whole expression and reaches the
// server; the structured filter's value travels as canonicalized.
func TestAccObjectTemplate_expressionEvaluatorBareEdit(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-expr")
	cfg := func(role string) string {
		return fmt.Sprintf(`
resource "midpoint_object_template" "t" {
  name = %q
  mapping = [yamldecode(<<-EOT
    name: m
    target:
      path: assignment
    expression:
      assignmentTargetSearch:
        targetType: RoleType
        filter:
          equal:
            path: name
            value: %q
  EOT
  )]
}
`, name, role)
	}
	const addr = "midpoint_object_template.t"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accSimpleGone(c),
		Steps: []resource.TestStep{
			{Config: cfg("first")},
			emptyPlanAfterRefresh(),
			{
				Config: cfg("second"),
				Check: func(st *terraform.State) error {
					body, err := accGet(c, "objectTemplates", st.RootModule().Resources[addr].Primary.Attributes["oid"])
					if err != nil {
						return err
					}
					got, err := json.Marshal(body["mapping"])
					if err != nil {
						return err
					}
					if !strings.Contains(string(got), "second") || strings.Contains(string(got), "first") {
						return fmt.Errorf("mapping = %s, want the filter on second", got)
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
		},
	})
}

// valuePolicyConfig is a value policy with a single limit written bare.
func valuePolicyConfig(name, limit string) string {
	return fmt.Sprintf(`
resource "midpoint_object" "p" {
  type = "valuePolicies"
  body = yamldecode(<<-EOT
    name: %s
    stringPolicy:
      limitations:
        minLength: 4
        limit:%s
  EOT
  )
}
`, name, limit)
}

// stringPolicy/limitations/limit reads as a list with no "@id": a single limit
// written bare settles.
func TestAccObject_valuePolicyLimitBare(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accCheckGone(c),
		Steps: []resource.TestStep{
			{Config: valuePolicyConfig(acctest.RandomWithPrefix("tf-acc-vp"), `
          description: digits
          minOccurs: 1
          characterClass:
            value: "0123456789"`)},
			emptyPlanAfterRefresh(),
		},
	})
}

// midPoint rejects a path inside limitations, so an edit of a limit replaces
// the whole of limitations.
func TestAccObject_valuePolicyLimitEdit(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-vp")
	// characterClass/value is a single string: midPoint 4.9 answers HTTP 500
	// to the replace of limitations when it carries a one-element list.
	limit := func(minOccurs int) string {
		return fmt.Sprintf(`
          description: digits
          minOccurs: %d
          characterClass:
            value: "0123456789"`, minOccurs)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accCheckGone(c),
		Steps: []resource.TestStep{
			{Config: valuePolicyConfig(name, limit(1))},
			emptyPlanAfterRefresh(),
			{
				Config: valuePolicyConfig(name, limit(2)),
				Check: func(st *terraform.State) error {
					body, err := accGet(c, "valuePolicies", st.RootModule().Resources["midpoint_object.p"].Primary.Attributes["oid"])
					if err != nil {
						return err
					}
					got, err := json.Marshal(body["stringPolicy"])
					if err != nil {
						return err
					}
					if !strings.Contains(string(got), `"minOccurs":2`) {
						return fmt.Errorf("stringPolicy = %s, want minOccurs 2", got)
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
		},
	})
}
