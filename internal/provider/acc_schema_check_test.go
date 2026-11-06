package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
)

// accCheckNoObject fails the test if restType holds an object called name.
func accCheckNoObject(t *testing.T, c *client.Client, restType, name string) {
	t.Helper()
	objs, _, err := c.Search(context.Background(), restType, fmt.Sprintf("name = %q", name), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 0 {
		t.Errorf("%s holds %s (oid %s), but the failed plan should have written nothing", restType, name, objs[0].OID)
	}
}

// accProviderWarning is the provider configuration for a directory run by
// accTerraform, with the checks downgraded to warnings.
const accProviderWarning = `terraform {
  required_providers {
    midpoint = { source = "brokenloop-org/midpoint" }
  }
}

provider "midpoint" {
  on_invalid_item = "warning"
}
`

// accWriteConfig writes a main.tf into a new directory and returns a runner
// for that directory that destroys what it created when the test ends.
func accWriteConfig(t *testing.T, env []string, cfg string) *accTerraform {
	t.Helper()
	dir := t.TempDir()
	body := accProviderWarning + cfg
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tf := newAccTerraform(t, env, dir)
	t.Cleanup(func() {
		if out, err := tf.try("destroy", "-auto-approve", "-no-color"); err != nil {
			t.Errorf("cleaning up: %v\n%s", err, out)
		}
	})
	return tf
}

// A typo in a free-form attribute fails the plan by default, naming the key,
// and nothing reaches the server.
func TestAccSchemaCheck_typoWritesNothing(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	roleName := acctest.RandomWithPrefix("tf-acc-typo-role")
	objectName := acctest.RandomWithPrefix("tf-acc-typo-object")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "midpoint_role" "r" {
  name  = %q
  extra = { descripton = "typo" }
}
`, roleName),
				ExpectError: regexp.MustCompile(`(?s)Invalid midPoint item.*descripton\s+is\s+not\s+an\s+item\s+of\s+RoleType`),
			},
			{
				Config: fmt.Sprintf(`
resource "midpoint_object" "o" {
  type = "roles"
  body = {
    name  = %q
    inducement = [{ ordr = 2 }]
  }
}
`, objectName),
				ExpectError: regexp.MustCompile(`(?s)Invalid midPoint item.*inducement\[0\]\.ordr\s+is\s+not\s+an\s+item\s+of\s+AssignmentType`),
			},
		},
	})
	accCheckNoObject(t, c, "roles", roleName)
	accCheckNoObject(t, c, "roles", objectName)
}

// connector must be there and takes only its own arguments, and both fail
// before anything reaches the server.
func TestAccSchemaCheck_connectorBlock(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	name := acctest.RandomWithPrefix("tf-acc-conn")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      fmt.Sprintf(`resource "midpoint_resource" "r" { name = %q }`, name),
				ExpectError: regexp.MustCompile(`connector`),
			},
			{
				Config: fmt.Sprintf(`
resource "midpoint_resource" "r" {
  name = %q
  connector {
    type    = %q
    verison = "1"
  }
}
`, name, accCSVConnectorType),
				ExpectError: regexp.MustCompile(`(?s)Unsupported argument.*verison`),
			},
		},
	})
	accCheckNoObject(t, c, "resources", name)
}

// accValueForm is a value form that a schema could rule out, and what the
// walker and midPoint do with it. These are the same on 4.9.9, 4.10.5 and 4.11.0.
type accValueForm struct {
	name, restType string
	body           string   // HCL with one %q for the object's name
	flagged        bool     // the walker reports it
	rejected       string   // when midPoint refuses the create, part of its message
	item           []string // the path of the item, to read what midPoint stored
	stored         string   // when midPoint takes it, what it stored; "" for nothing
}

var accValueForms = []accValueForm{
	{name: "enum-other-case", restType: "objectTemplates", item: []string{"mapping", "strength"}, stored: "strong", body: `{
    name    = %q
    mapping = [{
      name     = "m"
      strength = "STRONG"
      source   = [{ path = "givenName" }]
      target   = { path = "fullName" }
    }]
  }`},
	{name: "enum-constant-name", restType: "tasks", item: []string{"activity", "execution", "mode"}, stored: "dryRun", body: `{
    name           = %q
    executionState = "suspended"
    ownerRef       = { oid = "00000000-0000-0000-0000-000000000002", type = "UserType" }
    activity       = { execution = { mode = "DRY_RUN" } }
  }`},
	{name: "enum-mixed-case", restType: "roles", flagged: true, rejected: "IdempotenceType", body: `{
    name        = %q
    idempotence = "Conservative"
  }`},
	{name: "boolean-yes", restType: "roles", flagged: true, rejected: `"yes"`, body: `{
    name        = %q
    requestable = "yes"
  }`},
	{name: "boolean-TRUE", restType: "roles", item: []string{"requestable"}, stored: "true", body: `{
    name        = %q
    requestable = "TRUE"
  }`},
	{name: "integer-first", restType: "orgs", flagged: true, rejected: `"first"`, body: `{
    name         = %q
    displayOrder = "first"
  }`},
	// midPoint takes this and keeps nothing, so the setting is dead and stays
	// flagged.
	{name: "object-for-simple", restType: "roles", flagged: true, item: []string{"riskLevel"}, body: `{
    name      = %q
    riskLevel = { x = "y" }
  }`},
	{name: "two-for-single", restType: "roles", flagged: true, rejected: "single-valued", body: `{
    name      = %q
    riskLevel = ["a", "b"]
  }`},
}

// accWriteModes are the options a form is written with: midPoint's parsing
// could differ between a raw write and a full one.
var accWriteModes = []struct{ name, options string }{
	{"full", `["isImport"]`},
	{"raw", `["isImport", "raw"]`},
}

// TestAccValueForms pins what midPoint does with each value form, and that the
// walker flags exactly the forms that are refused or lost.
func TestAccValueForms(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	env := accBuildProvider(t)
	for _, form := range accValueForms {
		for _, mode := range accWriteModes {
			t.Run(form.name+"/"+mode.name, func(t *testing.T) {
				name := acctest.RandomWithPrefix("tf-acc-form")
				tf := accWriteConfig(t, env, fmt.Sprintf(`
resource "midpoint_object" "o" {
  type    = %q
  options = %s
  body    = %s
}
`, form.restType, mode.options, fmt.Sprintf(form.body, name)))
				plan := tf.run("plan", "-no-color")
				if got := accInvalidItemWarning.Match(plan); got != form.flagged {
					t.Errorf("flagged = %v, want %v:\n%s", got, form.flagged, plan)
				}
				out, err := tf.try("apply", "-auto-approve", "-no-color")
				if form.rejected != "" {
					accCheckRejected(t, c, form.restType, name, form.rejected, err)
					return
				}
				if err != nil {
					t.Fatalf("midPoint refused it: %v\n%s", err, out)
				}
				accCheckStored(t, c, form.restType, name, form.item, form.stored)
				// The configured form never equals the one midPoint returns.
				accCheckPlanStaysDirty(t, tf)
			})
		}
	}
}

// A mistyped key in extra is refused on create. On update it is lost: the apply
// succeeds, nothing is stored, and the plan keeps showing it. The exception is a
// non-raw update on 4.11, which midPoint fails with HTTP 500.
func TestAccValueForms_mistypedExtraKey(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	env := accBuildProvider(t)
	version, err := c.ServerVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	config := func(name, options, extra string) string {
		return fmt.Sprintf(`
resource "midpoint_role" "r" {
  name    = %q
  options = %s
  extra   = %s
}
`, name, options, extra)
	}
	for _, mode := range accWriteModes {
		t.Run("create/"+mode.name, func(t *testing.T) {
			name := acctest.RandomWithPrefix("tf-acc-form")
			tf := accWriteConfig(t, env, config(name, mode.options, `{ descripton = "typo" }`))
			_, err := tf.try("apply", "-auto-approve", "-no-color")
			accCheckRejected(t, c, "roles", name, "descripton", err)
		})
		t.Run("update/"+mode.name, func(t *testing.T) {
			name := acctest.RandomWithPrefix("tf-acc-form")
			tf := accWriteConfig(t, env, config(name, mode.options, `{ documentation = "ok" }`))
			tf.run("apply", "-auto-approve", "-no-color")
			accRewriteConfig(t, tf, config(name, mode.options, `{ documentation = "ok", descripton = "typo" }`))
			_, err := tf.try("apply", "-auto-approve", "-no-color")
			if strings.HasPrefix(version, "4.11") && mode.name == "full" {
				if err == nil || !strings.Contains(strings.Join(strings.Fields(err.Error()), " "), "attempt to modify an immutable") {
					t.Errorf("the apply returned %v, want midPoint's HTTP 500", err)
				}
			} else if err != nil {
				t.Errorf("the apply failed, want the typo lost without an error: %v", err)
			}
			accCheckStored(t, c, "roles", name, []string{"descripton"}, "")
			accCheckPlanStaysDirty(t, tf)
		})
	}
}

var accInvalidItemWarning = regexp.MustCompile(`Warning: Invalid midPoint item`)

// accCheckRejected checks that the apply failed with a message holding want and
// created nothing.
func accCheckRejected(t *testing.T, c *client.Client, restType, name, want string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("the apply succeeded, want midPoint to refuse it")
	}
	if !strings.Contains(strings.Join(strings.Fields(err.Error()), " "), want) {
		t.Errorf("the error does not hold %q: %v", want, err)
	}
	accCheckNoObject(t, c, restType, name)
}

// accCheckStored checks what midPoint holds at item of the object called name;
// want "" means nothing.
func accCheckStored(t *testing.T, c *client.Client, restType, name string, item []string, want string) {
	t.Helper()
	objs, _, err := c.Search(context.Background(), restType, fmt.Sprintf("name = %q", name), 1, nil)
	if err != nil || len(objs) != 1 {
		t.Fatalf("looking up %s: %v (found %d, want 1)", name, err, len(objs))
	}
	var cur any = objs[0].Body
	for _, key := range item {
		if l := asList(cur); len(l) > 0 {
			cur = l[0]
		}
		m, _ := cur.(map[string]any)
		cur = m[key]
	}
	got := ""
	if l := asList(cur); len(l) > 0 {
		got = fmt.Sprint(l[0])
	}
	if got != want {
		t.Errorf("midPoint stored %q at %v, want %q (object %v)", got, item, want, objs[0].Body)
	}
}

// accCheckPlanStaysDirty checks that the next plan succeeds and has changes.
func accCheckPlanStaysDirty(t *testing.T, tf *accTerraform) {
	t.Helper()
	out, err := tf.try("plan", "-no-color", "-detailed-exitcode")
	if err == nil {
		t.Errorf("the plan is clean, want it to keep showing the value:\n%s", out)
	} else if !strings.Contains(err.Error(), "exit status 2") {
		t.Error(err)
	}
}

// accRewriteConfig replaces the resources of a directory from accWriteConfig.
func accRewriteConfig(t *testing.T, tf *accTerraform, cfg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(tf.dir, "main.tf"), []byte(accProviderWarning+cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A problem found in a free-form attribute only warns with
// on_invalid_item = "warning", and the apply goes ahead.
func TestAccSchemaCheck_warningProceeds(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	env := accBuildProvider(t)
	name := acctest.RandomWithPrefix("tf-acc-warn")
	tf := accWriteConfig(t, env, fmt.Sprintf(`
resource "midpoint_role" "r" {
  name  = %q
  extra = { costCenter = { x = "y" } }
}
`, name))
	plan := tf.run("plan", "-no-color")
	if !accInvalidItemWarning.Match(plan) {
		t.Errorf("the plan did not warn:\n%s", plan)
	}
	tf.run("apply", "-auto-approve", "-no-color")
	accCheckStored(t, c, "roles", name, []string{"costCenter"}, "")
}

// State written by the released v0.2.0, where connector and schema were
// attributes, plans no changes in the block form.
func TestAccResource_stateFromV020PlansEmpty(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	fixtures := accCSVFixtures(t, c)
	// The provider under test takes the source the v0.2.0 state records.
	t.Setenv(resource.EnvTfAccProviderNamespace, "brokenloop-org")
	name := acctest.RandomWithPrefix("tf-acc-v020")
	cfg := func(connector, schema string) string {
		return fmt.Sprintf(`
resource "midpoint_resource" "csv" {
  name                    = %q
  connector_configuration = yamldecode(file(%q))
  schema_handling         = yamldecode(file(%q))
  %s
  %s
}
`, name, filepath.Join(fixtures.dir, "connector-configuration.yaml"), filepath.Join(fixtures.dir, "schema-handling.yaml"), connector, schema)
	}
	inner := fmt.Sprintf(`
    type    = %q
    version = %q
`, accCSVConnectorType, fixtures.connectorVersion)
	const constraints = `
    generation_constraints = { generateObjectClass = ["ri:AccountObjectClass"] }
`
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				ExternalProviders: map[string]resource.ExternalProvider{
					"midpoint": {Source: "brokenloop-org/midpoint", VersionConstraint: "0.2.0"},
				},
				Config: cfg("connector = {"+inner+"  }", "schema = {"+constraints+"  }"),
			},
			{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Config:                   cfg("connector {"+inner+"  }", "schema {"+constraints+"  }"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
