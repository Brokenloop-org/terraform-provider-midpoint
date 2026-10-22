package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/jplana/terraform-provider-midpoint/internal/mpstub"
)

// planDiagRecorder wraps the provider server and records the diagnostics of
// every PlanResourceChange response, which terraform-plugin-testing does not
// expose for warnings.
type planDiagRecorder struct {
	tfprotov6.ProviderServer
	rec *recordedDiags
}

type recordedDiags struct {
	mu    sync.Mutex
	diags []*tfprotov6.Diagnostic
}

func (r *planDiagRecorder) PlanResourceChange(ctx context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	resp, err := r.ProviderServer.PlanResourceChange(ctx, req)
	if resp != nil {
		r.rec.mu.Lock()
		r.rec.diags = append(r.rec.diags, resp.Diagnostics...)
		r.rec.mu.Unlock()
	}
	return resp, err
}

// warnings returns the details of the recorded warnings with summary.
func (r *recordedDiags) warnings(summary string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, d := range r.diags {
		if d.Severity == tfprotov6.DiagnosticSeverityWarning && d.Summary == summary {
			out = append(out, d.Detail)
		}
	}
	return out
}

// recordingProviderFactories are stubProviderFactories whose plan
// diagnostics are recorded in rec.
func recordingProviderFactories(t *testing.T, s *mpstub.Server, rec *recordedDiags) map[string]func() (tfprotov6.ProviderServer, error) {
	stubProviderFactories(t, s) // sets the provider environment
	return map[string]func() (tfprotov6.ProviderServer, error){
		"midpoint": func() (tfprotov6.ProviderServer, error) {
			srv, err := providerserver.NewProtocol6WithError(New("test")())()
			if err != nil {
				return nil, err
			}
			return &planDiagRecorder{ProviderServer: srv, rec: rec}, nil
		},
	}
}

// assertAdoptionPlan runs cfg as a plan-only step and checks that the plan
// warns with want and that planning sent nothing but GETs.
func assertAdoptionPlan(t *testing.T, s *mpstub.Server, cfg, want string) {
	t.Helper()
	rec := &recordedDiags{}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: recordingProviderFactories(t, s, rec),
		Steps: []resource.TestStep{{
			Config:             cfg,
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
	got := rec.warnings("Existing object will be adopted")
	if len(got) == 0 {
		t.Fatalf("no adoption warning in the plan")
	}
	for _, g := range got {
		if g != want {
			t.Errorf("adoption warning =\n%s\nwant\n%s", g, want)
		}
	}
	for _, r := range s.Requests() {
		if r.Method != "GET" {
			t.Errorf("planning sent %s %s; only GETs are allowed at plan time", r.Method, r.Path)
		}
	}
}

func TestRole_adoptPlanListsChanges(t *testing.T) {
	s := newTestStub(t)
	s.Put("roles", map[string]any{"oid": objRoleOID, "name": "Built-in", "description": "original", "riskLevel": "high"})
	cfg := fmt.Sprintf(`
resource "midpoint_role" "test" {
  oid            = %q
  adopt_existing = true
  name           = "Built-in"
  description    = "customized"
}
`, objRoleOID)
	assertAdoptionPlan(t, s, cfg, "midpoint_role roles/"+objRoleOID+" exists and will be adopted; "+
		"applying the configuration will send these changes:\n"+
		"  replace description\n"+
		"  replace riskLevel (removes it)")
}

func TestObject_adoptPlanListsChanges(t *testing.T) {
	s := newTestStub(t)
	s.Put("roles", map[string]any{"oid": objRoleOID, "name": "Built-in", "description": "original", "riskLevel": "high"})
	cfg := objectConfig(fmt.Sprintf(`
  type           = "roles"
  oid            = %q
  adopt_existing = true
  body = {
    name        = "Built-in"
    description = "customized"
  }
`, objRoleOID))
	assertAdoptionPlan(t, s, cfg, "midpoint_object roles/"+objRoleOID+" exists and will be adopted; "+
		"applying the configuration will send these changes:\n"+
		"  replace description\n"+
		"  replace riskLevel (removes it)")
}

func TestObject_adoptPlanNoChanges(t *testing.T) {
	s := newTestStub(t)
	s.Put("roles", map[string]any{"oid": objRoleOID, "name": "Built-in", "description": "original"})
	cfg := objectConfig(fmt.Sprintf(`
  type           = "roles"
  oid            = %q
  adopt_existing = true
  body = {
    name        = "Built-in"
    description = "original"
  }
`, objRoleOID))
	assertAdoptionPlan(t, s, cfg, "midpoint_object roles/"+objRoleOID+" exists and will be adopted; "+
		"applying the configuration will send no changes.")
}

func TestAdoptionWarning_capsAndFallsBack(t *testing.T) {
	e := &engine{spec: engineSpec{RestType: "roles"}}
	actual := map[string]any{"name": "r"}
	desired := map[string]any{"name": "r"}
	for i := range adoptionMaxChanges + 3 {
		desired[fmt.Sprintf("item%02d", i)] = "v"
	}
	got := e.adoptionWarning(context.Background(), "midpoint_object", "o", desired, actual)
	lines := strings.Split(got, "\n")
	if len(lines) != adoptionMaxChanges+2 || lines[len(lines)-1] != "and 3 more" {
		t.Fatalf("warning has %d lines, last %q; want %d change lines then \"and 3 more\":\n%s",
			len(lines), lines[len(lines)-1], adoptionMaxChanges, got)
	}
	if strings.Contains(got, "\"v\"") || strings.Contains(got, " v\n") {
		t.Fatalf("warning must not hold values:\n%s", got)
	}

	unknown := e.adoptionWarning(context.Background(), "midpoint_object", "o", nil, actual)
	if !strings.Contains(unknown, "cannot be listed") {
		t.Fatalf("unknown config warning = %q", unknown)
	}
}
