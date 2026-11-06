package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpstub"
)

const partialMessage = "provisioning to the resource failed"

// partialOn makes every request with the given method a partial error.
func partialOn(s *mpstub.Server, method string) {
	s.SetPartialError(func(r mpstub.Recorded) string {
		if r.Method == method && !strings.HasSuffix(r.Path, "/search") {
			return partialMessage
		}
		return ""
	})
}

func errorDetail(t *testing.T, diags diag.Diagnostics) string {
	t.Helper()
	if diags.WarningsCount() != 0 || diags.ErrorsCount() != 1 {
		t.Fatalf("diags = %v, want one error", diags)
	}
	return diags.Errors()[0].Detail()
}

func warningDetail(t *testing.T, diags diag.Diagnostics) string {
	t.Helper()
	if diags.HasError() || diags.WarningsCount() != 1 {
		t.Fatalf("diags = %v, want one warning", diags)
	}
	return diags.Warnings()[0].Detail()
}

func wantAll(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("detail %q does not mention %q", got, w)
		}
	}
}

// The error says the object exists, why it failed, and how to keep it.
func TestEngineCreate_partialErrorIsAnErrorWithTheOID(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	partialOn(s, http.MethodPost)

	oid, adopted, diags := e.create(context.Background(), objectState{OID: engRoleOID, Body: map[string]any{"name": "r"}})

	if oid != engRoleOID || adopted {
		t.Errorf("oid, adopted = %q, %v; want %q, false", oid, adopted, engRoleOID)
	}
	wantAll(t, errorDetail(t, diags), "roles/"+engRoleOID, partialMessage, "tainted", "terraform untaint")
}

// The object is not lost when midPoint generated its oid.
func TestEngineCreate_partialErrorReturnsGeneratedOID(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	partialOn(s, http.MethodPost)

	oid, _, diags := e.create(context.Background(), objectState{Body: map[string]any{"name": "r"}})

	if oid == "" || s.Get("roles", oid) == nil {
		t.Errorf("oid = %q, want the oid of the saved object", oid)
	}
	errorDetail(t, diags)
}

func TestEngineCreate_partialErrorAsWarning(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	e.partialWarning = true
	partialOn(s, http.MethodPost)

	oid, _, diags := e.create(context.Background(), objectState{OID: engRoleOID, Body: map[string]any{"name": "r"}})

	if oid != engRoleOID {
		t.Errorf("oid = %q, want %q", oid, engRoleOID)
	}
	wantAll(t, warningDetail(t, diags), "roles/"+engRoleOID, partialMessage)
}

func TestEngineUpdate_partialError(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.Put("roles", map[string]any{"oid": engRoleOID, "name": "r"})
	partialOn(s, http.MethodPatch)

	diags := e.update(context.Background(), engRoleOID, map[string]any{"name": "r", "description": "new"}, nil)

	wantAll(t, errorDetail(t, diags), "roles/"+engRoleOID, partialMessage)
}

func TestEngineUpdate_partialErrorAsWarning(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	e.partialWarning = true
	s.Put("roles", map[string]any{"oid": engRoleOID, "name": "r"})
	partialOn(s, http.MethodPatch)

	diags := e.update(context.Background(), engRoleOID, map[string]any{"name": "r", "description": "new"}, nil)

	wantAll(t, warningDetail(t, diags), "roles/"+engRoleOID, partialMessage)
}

// A delete stays an error in both modes: a warning would drop the object from
// state whether or not it is gone. The next refresh settles it.
func TestEngineDelete_partialErrorIsAlwaysAnError(t *testing.T) {
	for _, warning := range []bool{false, true} {
		t.Run(fmt.Sprintf("warning=%v", warning), func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, nil)
			e.partialWarning = warning
			s.Put("roles", map[string]any{"oid": engRoleOID, "name": "r"})
			partialOn(s, http.MethodDelete)

			diags := e.delete(context.Background(), engRoleOID, true)

			wantAll(t, errorDetail(t, diags), "roles/"+engRoleOID, partialMessage)
		})
	}
}

func partialResourceConfigs(desc string) map[string]string {
	return map[string]string{
		"midpoint_object.test": fmt.Sprintf(`
resource "midpoint_object" "test" {
  type = "roles"
  oid  = %q
  body = { name = "Partial role", description = %q }
}
`, engRoleOID, desc),
		"midpoint_role.test": fmt.Sprintf(`
resource "midpoint_role" "test" {
  oid         = %q
  name        = "Partial role"
  description = %q
}
`, engRoleOID, desc),
	}
}

// By default a create answered with a partial error fails, the object is in
// state, and the next plan replaces it because it is tainted.
func TestResource_partialErrorOnCreateTaints(t *testing.T) {
	for addr, cfg := range partialResourceConfigs("d") {
		t.Run(addr, func(t *testing.T) {
			s := newTestStub(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{
					{
						PreConfig:   func() { partialOn(s, http.MethodPost) },
						Config:      cfg,
						ExpectError: regexp.MustCompile(`terraform untaint`),
					},
					{
						PreConfig: func() { s.SetPartialError(nil) },
						Config:    cfg,
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
						},
					},
				},
			})
		})
	}
}

// With on_partial_error = "warning" the create succeeds and nothing is
// replaced.
func TestResource_partialErrorOnCreateAsWarning(t *testing.T) {
	const provider = `
provider "midpoint" {
  on_partial_error = "warning"
}
`
	for addr, cfg := range partialResourceConfigs("d") {
		t.Run(addr, func(t *testing.T) {
			s := newTestStub(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{
					{
						PreConfig: func() { partialOn(s, http.MethodPost) },
						Config:    provider + cfg,
					},
					{
						PreConfig: func() { s.SetPartialError(nil) },
						Config:    provider + cfg,
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)},
						},
					},
				},
			})
		})
	}
}

// A failed update does not taint: the change is saved, so the next plan is
// clean.
func TestResource_partialErrorOnUpdateDoesNotTaint(t *testing.T) {
	s := newTestStub(t)
	cfgs := func(desc string) string { return partialResourceConfigs(desc)["midpoint_object.test"] }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{Config: cfgs("before")},
			{
				PreConfig:   func() { partialOn(s, http.MethodPatch) },
				Config:      cfgs("after"),
				ExpectError: regexp.MustCompile(partialMessage),
			},
			{
				PreConfig: func() { s.SetPartialError(nil) },
				Config:    cfgs("after"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("midpoint_object.test", plancheck.ResourceActionNoop)},
				},
			},
		},
	})
}

func TestProvider_onPartialErrorRejectsOtherValues(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
provider "midpoint" {
  on_partial_error = "ignore"
}
` + partialResourceConfigs("d")["midpoint_object.test"],
			ExpectError: regexp.MustCompile(`(?s)on_partial_error.*value must be one of`),
		}},
	})
}
