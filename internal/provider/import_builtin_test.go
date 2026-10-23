package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// importedAdopt imports id as a typeName resource through the provider
// server and returns the adopt_existing the imported state records.
func importedAdopt(t *testing.T, typeName string, newResource func() fwresource.Resource, id string) bool {
	t.Helper()
	ctx := context.Background()
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.ImportResourceState(ctx, &tfprotov6.ImportResourceStateRequest{TypeName: typeName, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("import %s %s: %s: %s", typeName, id, d.Summary, d.Detail)
		}
	}
	if len(resp.ImportedResources) != 1 {
		t.Fatalf("import %s %s: %d resources", typeName, id, len(resp.ImportedResources))
	}
	var sr fwresource.SchemaResponse
	newResource().Schema(ctx, fwresource.SchemaRequest{}, &sr)
	state, err := resp.ImportedResources[0].State.Unmarshal(sr.Schema.Type().TerraformType(ctx))
	if err != nil {
		t.Fatal(err)
	}
	var attrs map[string]tftypes.Value
	if err := state.As(&attrs); err != nil {
		t.Fatal(err)
	}
	var adopt bool
	if err := attrs["adopt_existing"].As(&adopt); err != nil {
		t.Fatal(err)
	}
	return adopt
}

// An imported built-in is recorded as adopted, so that the adopt_existing =
// true its configuration carries (the converter writes it) plans clean; any
// other import records false.
func TestImportState_builtinRecordedAsAdopted(t *testing.T) {
	const builtin = "00000000-0000-0000-0000-000000000328"
	const custom = "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02"
	typed := map[string]func() fwresource.Resource{
		"midpoint_archetype": NewArchetypeResource, "midpoint_role": NewRoleResource,
		"midpoint_service": NewServiceResource, "midpoint_org": NewOrgResource,
		"midpoint_object_template": NewObjectTemplateResource, "midpoint_mark": NewMarkResource,
		"midpoint_task": NewTaskResource, "midpoint_resource": NewResourceResource,
	}
	for name, newResource := range typed {
		if !importedAdopt(t, name, newResource, builtin) {
			t.Errorf("%s: built-in imported with adopt_existing = false", name)
		}
		if importedAdopt(t, name, newResource, custom) {
			t.Errorf("%s: custom object imported with adopt_existing = true", name)
		}
	}
	if !importedAdopt(t, "midpoint_object", NewObjectResource, "archetypes/"+builtin) {
		t.Error("midpoint_object: built-in imported with adopt_existing = false")
	}
	if importedAdopt(t, "midpoint_object", NewObjectResource, "functionLibraries/"+custom) {
		t.Error("midpoint_object: custom object imported with adopt_existing = true")
	}
}
