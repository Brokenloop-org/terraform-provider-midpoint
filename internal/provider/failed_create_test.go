package provider

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpstub"
)

// failPostAfterWrite makes every create save the object and then answer status
// without a Location, as midPoint does when provisioning fails fatally.
func failPostAfterWrite(s *mpstub.Server, status int) {
	s.SetFailAfterWrite(func(r mpstub.Recorded) int {
		if r.Method == http.MethodPost && !isSearch(r) {
			return status
		}
		return 0
	})
}

// failPost makes every create fail with status before anything is saved.
func failPost(s *mpstub.Server, status int) {
	s.SetFault(func(r mpstub.Recorded) int {
		if r.Method == http.MethodPost && !isSearch(r) {
			return status
		}
		return 0
	})
}

func isSearch(r mpstub.Recorded) bool { return strings.HasSuffix(r.Path, "/search") }

// The provider picks the oid of a new object, so that it can find the object
// when midPoint saves it but answers without a Location; it does not read the
// object first.
func TestEngineCreate_sendsAGeneratedOID(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)

	oid, _, diags := e.create(context.Background(), objectState{Body: map[string]any{"name": "r"}})

	if diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	if _, err := uuid.Parse(oid); err != nil {
		t.Errorf("oid = %q, want a UUID", oid)
	}
	posts := requests(s, http.MethodPost)
	if len(posts) != 1 {
		t.Fatalf("POSTs = %v, want one", posts)
	}
	sent, _ := posts[0].Body["role"].(map[string]any)["oid"].(string)
	if sent != oid {
		t.Errorf("POST body oid = %q, want the returned oid %q", sent, oid)
	}
	if gets := requests(s, http.MethodGet); len(gets) != 0 {
		t.Errorf("GETs = %v, want none for a generated oid", gets)
	}
}

// midPoint answers 502 without a Location, but saved the object: it is
// returned with an error, so that it is recorded (and tainted).
func TestEngineCreate_serverErrorAfterSaveReturnsTheOID(t *testing.T) {
	for name, oid := range map[string]string{"generated": "", "configured": engRoleOID} {
		t.Run(name, func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, nil)
			failPostAfterWrite(s, http.StatusBadGateway)

			got, adopted, diags := e.create(context.Background(), objectState{OID: oid, Body: map[string]any{"name": "r"}})

			if got == "" || adopted || s.Get("roles", got) == nil {
				t.Fatalf("oid, adopted = %q, %v; want the oid of the saved object", got, adopted)
			}
			if oid != "" && got != oid {
				t.Errorf("oid = %q, want %q", got, oid)
			}
			wantAll(t, errorDetail(t, diags), "roles/"+got, "HTTP 502", "tainted", "terraform untaint")
		})
	}
}

func TestEngineCreate_serverErrorAfterSaveAsWarning(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	e.partialWarning = true
	failPostAfterWrite(s, http.StatusBadGateway)

	oid, _, diags := e.create(context.Background(), objectState{Body: map[string]any{"name": "r"}})

	if oid == "" || s.Get("roles", oid) == nil {
		t.Fatalf("oid = %q, want the oid of the saved object", oid)
	}
	wantAll(t, warningDetail(t, diags), "roles/"+oid, "HTTP 502")
}

// A server error that saved nothing stays an ordinary failure: nothing to
// record.
func TestEngineCreate_serverErrorWithoutSaveReturnsNoOID(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	e.partialWarning = true
	failPost(s, http.StatusBadGateway)

	oid, _, diags := e.create(context.Background(), objectState{Body: map[string]any{"name": "r"}})

	if oid != "" {
		t.Errorf("oid = %q, want none", oid)
	}
	wantAll(t, errorDetail(t, diags), "HTTP 502")
}

// A 4xx (a conflict, say) is not followed up: an object found under the oid
// could be one that existed before.
func TestEngineCreate_clientErrorIsNotFollowedUp(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	failPostAfterWrite(s, http.StatusConflict)

	oid, _, diags := e.create(context.Background(), objectState{Body: map[string]any{"name": "r"}})

	if oid != "" {
		t.Errorf("oid = %q, want none", oid)
	}
	errorDetail(t, diags)
	if gets := requests(s, http.MethodGet); len(gets) != 0 {
		t.Errorf("GETs = %v, want none after a 4xx", gets)
	}
}

// generatedOIDConfigs are the configs of partialResourceConfigs without an
// oid, so that the provider generates it.
func generatedOIDConfigs() map[string]string {
	return map[string]string{
		"midpoint_object.test": `
resource "midpoint_object" "test" {
  type = "roles"
  body = { name = "Failed role" }
}
`,
		"midpoint_role.test": `
resource "midpoint_role" "test" {
  name = "Failed role"
}
`,
	}
}

// midPoint answers 502 without a Location for a create it saved: the object
// is in state, tainted, and the next plan replaces it instead of creating a
// second one.
func TestResource_serverErrorAfterSaveTaints(t *testing.T) {
	for addr, cfg := range generatedOIDConfigs() {
		t.Run(addr, func(t *testing.T) {
			s := newTestStub(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{
					{
						PreConfig:   func() { failPostAfterWrite(s, http.StatusBadGateway) },
						Config:      cfg,
						ExpectError: regexp.MustCompile(`terraform untaint`),
					},
					{
						PreConfig: func() { s.SetFailAfterWrite(nil) },
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

// A 502 that saved nothing leaves nothing in state: the next plan creates.
func TestResource_serverErrorWithoutSaveRecordsNothing(t *testing.T) {
	for addr, cfg := range generatedOIDConfigs() {
		t.Run(addr, func(t *testing.T) {
			s := newTestStub(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: stubProviderFactories(t, s),
				Steps: []resource.TestStep{
					{
						PreConfig:   func() { failPost(s, http.StatusBadGateway) },
						Config:      cfg,
						ExpectError: regexp.MustCompile(`Cannot create object`),
					},
					{
						PreConfig: func() { s.SetFault(nil) },
						Config:    cfg,
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
						},
					},
				},
			})
		})
	}
}

// When the follow-up read fails too, the error names the oid, so that an
// object midPoint saved can be imported.
func TestEngineCreate_serverErrorUncheckedNamesTheOID(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.SetFault(func(r mpstub.Recorded) int {
		if r.Method == http.MethodPost || r.Method == http.MethodGet {
			return http.StatusBadGateway
		}
		return 0
	})

	oid, _, diags := e.create(context.Background(), objectState{Body: map[string]any{"name": "r"}})

	if oid != "" {
		t.Errorf("oid = %q, want none", oid)
	}
	posts := requests(s, http.MethodPost)
	sent, _ := posts[0].Body["role"].(map[string]any)["oid"].(string)
	wantAll(t, errorDetail(t, diags), "HTTP 502", "roles/"+sent, "import")
}
