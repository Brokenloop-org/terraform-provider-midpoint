package provider

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpschema"
)

const warningProvider = `
provider "midpoint" {
  on_invalid_item = "warning"
}
`

// allSchemaChecks are the checks of every resource that has any.
func allSchemaChecks() map[string]schemaChecks {
	out := map[string]schemaChecks{
		"resource":        resourceSchemaChecks,
		"task":            taskSchemaChecks,
		"object_template": objectTemplateSchemaChecks,
		"mark":            markSchemaChecks,
	}
	for _, k := range []focusKind{roleKind, serviceKind, orgKind, archetypeKind} {
		out[k.Name] = focusSchemaChecks(k, focusRootTypes[k.Name])
	}
	return out
}

// A wrong row in a mapping would make the check silently do nothing.
func TestSchemaCheck_targetsResolve(t *testing.T) {
	for _, line := range []string{"4.9", "4.10", "4.11"} {
		table, ok := mpschema.For(line)
		if !ok {
			t.Fatalf("no table for %s", line)
		}
		for name, checks := range allSchemaChecks() {
			for _, c := range checks.Dynamics {
				if _, ok := table.Descend(checks.RootType, c.Items...); !ok {
					t.Errorf("%s: %s: %s %v does not resolve", line, name, checks.RootType, c.Items)
				}
			}
			if checks.Blocks {
				for _, c := range assignmentBlockChecks {
					if _, ok := table.Descend(assignmentType, c.Items...); !ok {
						t.Errorf("%s: %s: %s %v does not resolve", line, name, assignmentType, c.Items)
					}
				}
			}
		}
		for typ, el := range map[string]string{"roles": "RoleType", "tasks": "TaskType", "resources": "ResourceType"} {
			if got := table.Elements[singularForTest(typ)]; got != el {
				t.Errorf("%s: element for %s = %q, want %q", line, typ, got, el)
			}
		}
	}
}

func singularForTest(restType string) string {
	return map[string]string{"roles": "role", "tasks": "task", "resources": "resource"}[restType]
}

const roleHead = `
resource "midpoint_role" "r" {
  name = "r"
`

func resourceCfg(attrs string) string {
	return `
resource "midpoint_resource" "r" {
  name = "r"
  connector {
    type = "com.evolveum.polygon.connector.csv.CsvConnector"
  }
` + attrs + `
}
`
}

// Each row has one typo in one checked attribute; the plan must fail on it
// and nothing may reach the server.
func TestSchemaCheck_typoFails(t *testing.T) {
	rows := []struct{ name, cfg, want string }{
		{"role extra", roleHead + `  extra = { descripton = "x" }
}`, `(?s)extra.*descripton.*RoleType.*midPoint 4\.9`},
		{"role authorization", roleHead + `  authorization = [{ actoin = ["x"] }]
}`, `(?s)authorization.*actoin`},
		{"service extra", `
resource "midpoint_service" "r" {
  name  = "r"
  extra = { descripton = "x" }
}`, `(?s)descripton.*ServiceType`},
		{"org extra", `
resource "midpoint_org" "r" {
  name  = "r"
  extra = { descripton = "x" }
}`, `(?s)descripton.*OrgType`},
		{"archetype extra", `
resource "midpoint_archetype" "r" {
  name  = "r"
  extra = { descripton = "x" }
}`, `(?s)descripton.*ArchetypeType`},
		{"archetype policy", `
resource "midpoint_archetype" "r" {
  name             = "r"
  archetype_policy = { dsplay = { label = "x" } }
}`, `(?s)archetype_policy.*dsplay`},
		{"inducement extra", roleHead + `  inducement {
    extra = jsonencode({ descripton = "x" })
  }
}`, `(?s)inducement\[0\]\.extra.*descripton.*AssignmentType`},
		{"assignment condition", roleHead + `  assignment {
    condition = jsonencode({ expresion = {} })
  }
}`, `(?s)assignment\[0\]\.condition.*expresion`},
		{"assignment policy_rule", roleHead + `  assignment {
    policy_rule = jsonencode({ constrants = {} })
  }
}`, `(?s)assignment\[0\]\.policy_rule.*constrants`},
		{"construction body", roleHead + `  inducement {
    construction {
      resource = "7e500000-0000-4000-8000-0000000000c1"
      body     = jsonencode({ attribut = [] })
    }
  }
}`, `(?s)inducement\[0\]\.construction\.body.*attribut`},
		{"resource schema_handling", resourceCfg(`  schema_handling = { objectTyp = [] }`), `(?s)schema_handling.*objectTyp.*ResourceType|schema_handling.*objectTyp`},
		{"resource synchronization", resourceCfg(`  synchronization = { objectSynchronizaton = [] }`), `(?s)synchronization.*objectSynchronizaton`},
		{"resource capabilities", resourceCfg(`  capabilities = { crate = { enabled = false } }`), `(?s)capabilities.*crate`},
		{"resource generation_constraints", resourceCfg(`
  schema {
    generation_constraints = { generateObjectClas = ["ri:AccountObjectClass"] }
  }`), `(?s)generation_constraints.*generateObjectClas`},
		{"resource extra", resourceCfg(`  extra = { descripton = "x" }`), `(?s)extra.*descripton`},
		{"resource enum", resourceCfg(`  schema_handling = { objectType = [{
    kind = "account"
    attribute = [{ ref = "ri:login", inbound = [{ strength = "strng", target = { path = "name" } }] }]
  }] }`), `(?s)schema_handling.*strng.*strong`},
		{"task schedule", `
resource "midpoint_task" "t" {
  name     = "t"
  schedule = { interva = 60 }
}`, `(?s)schedule.*interva`},
		{"task activity", `
resource "midpoint_task" "t" {
  name     = "t"
  activity = { workk = {} }
}`, `(?s)activity.*workk`},
		{"task extra", `
resource "midpoint_task" "t" {
  name  = "t"
  extra = { descripton = "x" }
}`, `(?s)extra.*descripton.*TaskType`},
		{"template mapping", `
resource "midpoint_object_template" "t" {
  name    = "t"
  mapping = [{ strengh = "strong" }]
}`, `(?s)mapping.*strengh`},
		{"template item", `
resource "midpoint_object_template" "t" {
  name = "t"
  item = [{ reff = "name" }]
}`, `(?s)item.*reff`},
		{"template iteration", `
resource "midpoint_object_template" "t" {
  name                    = "t"
  iteration_specification = { maxIteration = 3 }
}`, `(?s)iteration_specification.*maxIteration`},
		{"template extra", `
resource "midpoint_object_template" "t" {
  name  = "t"
  extra = { descripton = "x" }
}`, `(?s)extra.*descripton.*ObjectTemplateType`},
		{"mark display", `
resource "midpoint_mark" "m" {
  name    = "m"
  display = { labl = "x" }
}`, `(?s)display.*labl`},
		{"mark event", `
resource "midpoint_mark" "m" {
  name  = "m"
  event = { enabledByDefaul = true }
}`, `(?s)event.*enabledByDefaul`},
		{"mark extra", `
resource "midpoint_mark" "m" {
  name  = "m"
  extra = { descripton = "x" }
}`, `(?s)extra.*descripton.*MarkType`},
		{"object body", `
resource "midpoint_object" "o" {
  type = "roles"
  body = { name = "o", descripton = "x" }
}`, `(?s)body.*descripton.*RoleType`},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s := newTestStub(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{{
					Config:      row.cfg,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(row.want),
				}},
			})
			for _, m := range []string{"POST", "PATCH", "PUT", "DELETE"} {
				if got := requests(s, m); len(got) != 0 {
					t.Errorf("%s requests = %v, want none", m, got)
				}
			}
		})
	}
}

func TestSchemaCheck_extraTypoFails(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: roleHead + `  extra = { descripton = "x" }
}`,
			ExpectError: regexp.MustCompile(`(?s)Invalid midPoint item.*descripton.*RoleType.*midPoint 4\.9`),
		}},
	})
	if got := requests(s, "POST"); len(got) != 0 {
		t.Errorf("POST requests = %v, want none", got)
	}
}

func TestSchemaCheck_warningMode(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: warningProvider + roleHead + `  extra = { descripton = "x" }
}`,
		}},
	})
	var creates int
	for _, r := range requests(s, "POST") {
		if r.Path == "/roles" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("role creates = %d, want 1", creates)
	}
}

func TestProvider_onInvalidItemRejectsOtherValues(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
provider "midpoint" {
  on_invalid_item = "ignore"
}
` + partialResourceConfigs("d")["midpoint_object.test"],
			ExpectError: regexp.MustCompile(`(?s)on_invalid_item.*value must be one of`),
		}},
	})
}

func TestSchemaCheck_onUpdate(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: roleHead + `  extra = { subtype = ["x"] }
}`},
			{
				Config: roleHead + `  extra = { subtype = ["x"], descripton = "y" }
}`,
				ExpectError: regexp.MustCompile(`(?s)descripton.*RoleType`),
			},
		},
	})
	if got := requests(s, "PATCH"); len(got) != 0 {
		t.Errorf("PATCH requests = %v, want none", got)
	}
}

// Checks read the configuration, not the state: an item the server holds
// that the configuration does not set is not flagged.
func TestSchemaCheck_importedExtraNotFlagged(t *testing.T) {
	s := newTestStub(t)
	oid := s.Put("roles", map[string]any{"name": "r", "serverOnlyItem": "x"})
	cfg := roleHead + `}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config:             cfg,
				ResourceName:       "midpoint_role.r",
				ImportState:        true,
				ImportStateId:      oid,
				ImportStatePersist: true,
			},
			// The config is authoritative, so the plan removes the item; what
			// matters is that it is not reported as invalid.
			{Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

func TestSchemaCheck_partlyUnknown(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
resource "terraform_data" "d" {
  input = "from elsewhere"
}
` + roleHead + `  extra = {
    subtype     = [terraform_data.d.output]
    descripton  = "x"
  }
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)descripton.*RoleType`),
		}},
	})
}

// A whole attribute that is unknown at plan cannot be judged.
func TestSchemaCheck_wholeAttributeUnknown(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
resource "terraform_data" "d" {
  input = { descripton = "x" }
}
` + roleHead + `  extra = terraform_data.d.output
}`,
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

// A key set to null is absent: the provider never sends it.
func TestSchemaCheck_nullKeyNotFlagged(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: roleHead + `  extra = { notInThisLine = null }
}`,
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

// extension and connector_configuration hold what each deployment defines,
// so they are never checked.
func TestSchemaCheck_extensionAndConnectorConfigurationUnchecked(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
resource "midpoint_role" "r" {
  name      = "r"
  extension = { anythingGoes = "x" }
}
` + resourceCfg(`  connector_configuration = { configurationProperties = { notARealKey = "x" }, madeUp = "y" }`),
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

// A destroy plan has no configuration worth checking: ModifyPlan returns
// before reading it.
func TestSchemaCheck_destroySkipped(t *testing.T) {
	table, _ := mpschema.For("4.9")
	r := NewRoleResource().(*typedResource[roleLikeState])
	r.data = &providerData{SchemaTable: table}
	var sr fwresource.SchemaResponse
	r.Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	null := tftypes.NewValue(sr.Schema.Type().TerraformType(context.Background()), nil)
	req := fwresource.ModifyPlanRequest{
		Config: tfsdk.Config{Raw: null, Schema: sr.Schema},
		Plan:   tfsdk.Plan{Raw: null, Schema: sr.Schema},
		State:  tfsdk.State{Raw: null, Schema: sr.Schema},
	}
	resp := &fwresource.ModifyPlanResponse{}
	r.ModifyPlan(context.Background(), req, resp)
	if len(resp.Diagnostics) != 0 {
		t.Errorf("diags = %v, want none", resp.Diagnostics)
	}
}

// configureProvider runs Configure against the stub with an empty provider
// block (the connection comes from the environment).
func configureProvider(t *testing.T) (*providerData, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	p := New("test")()
	var sr fwprovider.SchemaResponse
	p.Schema(ctx, fwprovider.SchemaRequest{}, &sr)
	vals := map[string]tftypes.Value{}
	for name, a := range sr.Schema.GetAttributes() {
		vals[name] = tftypes.NewValue(a.GetType().TerraformType(ctx), nil)
	}
	raw := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), vals)
	resp := &fwprovider.ConfigureResponse{}
	p.Configure(ctx, fwprovider.ConfigureRequest{Config: tfsdk.Config{Raw: raw, Schema: sr.Schema}}, resp)
	data, _ := resp.ResourceData.(*providerData)
	return data, resp.Diagnostics
}

func TestSchemaCheck_unsupportedVersion(t *testing.T) {
	t.Run("a line without a table warns once and skips the checks", func(t *testing.T) {
		s := newTestStub(t)
		s.SetNodeVersion("4.12")
		stubProviderFactories(t, s)
		data, diags := configureProvider(t)
		if diags.HasError() || diags.WarningsCount() != 1 || data == nil || data.SchemaTable != nil {
			t.Fatalf("data = %+v, diags = %v, want one warning and no table", data, diags)
		}
		wantAll(t, diags.Warnings()[0].Detail(), "4.12", "4.9, 4.10 and 4.11")

		// The typo applies.
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: stubProviderFactories(t, s),
			Steps: []resource.TestStep{{Config: roleHead + `  extra = { descripton = "x" }
}`}},
		})
	})
	t.Run("a suffix does not matter", func(t *testing.T) {
		s := newTestStub(t)
		s.SetNodeVersion("4.9.3-SNAPSHOT")
		stubProviderFactories(t, s)
		data, diags := configureProvider(t)
		if diags.WarningsCount() != 0 || data == nil || data.SchemaTable == nil || data.SchemaTable.Line != "4.9" {
			t.Fatalf("data = %+v, diags = %v, want the 4.9 table and no warning", data, diags)
		}
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: stubProviderFactories(t, s),
			Steps: []resource.TestStep{{
				Config: roleHead + `  extra = { descripton = "x" }
}`,
				ExpectError: regexp.MustCompile(`descripton`),
			}},
		})
	})
	t.Run("an unreadable version warns and skips", func(t *testing.T) {
		s := newTestStub(t)
		s.SetNodeVersion("")
		stubProviderFactories(t, s)
		data, diags := configureProvider(t)
		if diags.HasError() || diags.WarningsCount() != 1 || data == nil || data.SchemaTable != nil {
			t.Fatalf("data = %+v, diags = %v, want one warning and no table", data, diags)
		}
	})
}
