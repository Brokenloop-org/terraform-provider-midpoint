package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpstub"
)

// A write held for approval is indistinguishable from a success in midPoint's
// response, so these tests only assert on what the provider observes.

func changedRole() map[string]any {
	d := desiredRole()
	d["description"] = "configured"
	return d
}

func TestReadAfterWrite_heldUpdate(t *testing.T) {
	tests := []struct {
		name         string
		strict       bool
		created      bool
		wantErrors   int
		wantWarnings int
	}{
		{name: "warns by default", wantWarnings: 1},
		{name: "strict fails the update", strict: true, wantErrors: 1},
		// An error would taint a created or adopted object and replace it.
		{name: "strict still only warns after a create", strict: true, created: true, wantWarnings: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, nil)
			e.failOnUnappliedWrite = tt.strict
			s.Put("roles", seededRole())
			s.HoldWrites(true)
			desired := changedRole()

			diags := e.update(context.Background(), engRoleOID, desired, nil)
			noErrors(t, diags)
			if len(requests(s, http.MethodPatch)) != 1 {
				t.Fatalf("want one PATCH, got %d", len(requests(s, http.MethodPatch)))
			}
			diags = e.readAfterWrite(context.Background(), engRoleOID, desired, tt.created)

			if got := len(diags.Errors()); got != tt.wantErrors {
				t.Fatalf("errors = %d, want %d: %s", got, tt.wantErrors, diagText(diags))
			}
			if got := len(diags.Warnings()); got != tt.wantWarnings {
				t.Fatalf("warnings = %d, want %d: %s", got, tt.wantWarnings, diagText(diags))
			}
			text := diagText(diags)
			for _, want := range []string{"description", "approval", "normalized"} {
				if !strings.Contains(text, want) {
					t.Errorf("diagnostic does not mention %q: %s", want, text)
				}
			}
		})
	}
}

func TestReadAfterWrite_strictAcceptsAnAppliedWrite(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	e.failOnUnappliedWrite = true
	s.Put("roles", seededRole())
	desired := changedRole()

	noErrors(t, e.update(context.Background(), engRoleOID, desired, nil))
	diags := e.readAfterWrite(context.Background(), engRoleOID, desired, false)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %s", diagText(diags))
	}
}

func TestEngineDelete_objectStillThere(t *testing.T) {
	tests := []struct {
		name         string
		strict       bool
		wantErrors   int
		wantWarnings int
	}{
		{name: "warns by default", wantWarnings: 1},
		{name: "strict fails the delete", strict: true, wantErrors: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, nil)
			e.failOnUnappliedWrite = tt.strict
			s.Put("roles", seededRole())
			s.HoldWrites(true)

			diags := e.delete(context.Background(), engRoleOID, true)

			if got := len(diags.Errors()); got != tt.wantErrors {
				t.Fatalf("errors = %d, want %d: %s", got, tt.wantErrors, diagText(diags))
			}
			if got := len(diags.Warnings()); got != tt.wantWarnings {
				t.Fatalf("warnings = %d, want %d: %s", got, tt.wantWarnings, diagText(diags))
			}
			if text := diagText(diags); !strings.Contains(text, "approval") {
				t.Errorf("diagnostic does not mention approval: %s", text)
			}
		})
	}
}

func TestEngineDelete_strictAcceptsARealDelete(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	e.failOnUnappliedWrite = true
	s.Put("roles", seededRole())

	if diags := e.delete(context.Background(), engRoleOID, true); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %s", diagText(diags))
	}
}

// The delete itself succeeded; a failed check afterwards must not fail it.
func TestEngineDelete_failedCheckIsNotAFailedDelete(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	e.failOnUnappliedWrite = true
	s.Put("roles", seededRole())
	s.SetFault(func(r mpstub.Recorded) int {
		if r.Method == http.MethodGet {
			return http.StatusInternalServerError
		}
		return 0
	})

	if diags := e.delete(context.Background(), engRoleOID, true); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %s", diagText(diags))
	}
}

func TestEngineCreate_held(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.HoldWrites(true)

	_, _, diags := e.create(context.Background(), objectState{Body: desiredRole()})

	if !diags.HasError() {
		t.Fatal("a create that returns no object ID must fail")
	}
	if text := diagText(diags); !strings.Contains(text, "approval") {
		t.Errorf("error does not mention approval: %s", text)
	}
	if text := diagText(diags); strings.Count(text, "creating roles") != 1 {
		t.Errorf("error repeats its prefix: %s", text)
	}
}

// heldObjectUpdate runs midpoint_object Update against a held write.
func heldObjectUpdate(t *testing.T, strict bool) (req resource.UpdateRequest, resp *resource.UpdateResponse) {
	t.Helper()
	ctx := context.Background()
	s := mpstub.New()
	t.Cleanup(s.Close)
	c, err := client.New(client.Config{URL: s.URL(), Username: "administrator", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	s.Put("roles", seededRole())
	s.HoldWrites(true)

	r := &objectResource{data: &providerData{Client: c, FailOnUnappliedWrite: strict}}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	value := func(description string) tftypes.Value {
		bodyType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"name": tftypes.String, "description": tftypes.String}}
		attrs := map[string]tftypes.Value{}
		for name, typ := range objType.AttributeTypes {
			attrs[name] = tftypes.NewValue(typ, nil)
		}
		attrs["id"] = tftypes.NewValue(tftypes.String, engRoleOID)
		attrs["oid"] = tftypes.NewValue(tftypes.String, engRoleOID)
		attrs["type"] = tftypes.NewValue(tftypes.String, "roles")
		attrs["name"] = tftypes.NewValue(tftypes.String, "r1")
		attrs["body"] = tftypes.NewValue(bodyType, map[string]tftypes.Value{
			"name":        tftypes.NewValue(tftypes.String, "r1"),
			"description": tftypes.NewValue(tftypes.String, description),
		})
		return tftypes.NewValue(objType, attrs)
	}
	req = resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: value("configured")},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: value("d")},
	}
	resp = &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objType, nil)}}
	r.Update(ctx, req, resp)
	return req, resp
}

func TestObjectUpdate_heldStrictKeepsPriorState(t *testing.T) {
	req, resp := heldObjectUpdate(t, true)
	if !resp.Diagnostics.HasError() {
		t.Fatalf("want an error, got: %s", diagText(diag.Diagnostics(resp.Diagnostics)))
	}
	if !resp.State.Raw.Equal(req.State.Raw) {
		t.Errorf("state = %s, want the prior state %s", resp.State.Raw, req.State.Raw)
	}
}

func TestObjectUpdate_heldDefaultKeepsConfiguredState(t *testing.T) {
	req, resp := heldObjectUpdate(t, false)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %s", diagText(diag.Diagnostics(resp.Diagnostics)))
	}
	if !resp.State.Raw.Equal(req.Plan.Raw) {
		t.Errorf("state = %s, want the planned state %s", resp.State.Raw, req.Plan.Raw)
	}
}
