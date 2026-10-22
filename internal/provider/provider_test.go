package provider

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// testAccProtoV6ProviderFactories instantiates the provider for acceptance
// tests using the protocol version 6 server.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"midpoint": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck skips the test unless TF_ACC is set and fails it when the
// connection environment variables are missing.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests skipped unless TF_ACC is set")
	}
	for _, name := range []string{"MIDPOINT_URL", "MIDPOINT_USERNAME", "MIDPOINT_PASSWORD"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s must be set for acceptance tests", name)
		}
	}
}

func TestProviderSchema(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	resp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	for _, name := range []string{"url", "username", "password", "insecure", "ca_bundle", "default_options"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
	if !resp.Schema.Attributes["password"].IsSensitive() {
		t.Error("password must be sensitive")
	}
}

// configure runs the provider's Configure with the given attribute values;
// attributes not present in vals are null.
func configure(t *testing.T, vals map[string]tftypes.Value) *provider.ConfigureResponse {
	t.Helper()
	ctx := context.Background()
	p := New("test")()
	schemaResp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, schemaResp)
	objType := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	attrs := map[string]tftypes.Value{}
	for name, typ := range objType.AttributeTypes {
		if v, ok := vals[name]; ok {
			attrs[name] = v
		} else {
			attrs[name] = tftypes.NewValue(typ, nil)
		}
	}
	req := provider.ConfigureRequest{Config: tfsdk.Config{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(objType, attrs),
	}}
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, req, resp)
	return resp
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"MIDPOINT_URL", "MIDPOINT_USERNAME", "MIDPOINT_PASSWORD", "MIDPOINT_INSECURE"} {
		t.Setenv(name, "")
	}
}

func TestProviderConfigure_attributes(t *testing.T) {
	clearEnv(t)
	optsType := tftypes.Map{ElementType: tftypes.List{ElementType: tftypes.String}}
	resp := configure(t, map[string]tftypes.Value{
		"url":      tftypes.NewValue(tftypes.String, "http://localhost:8080"),
		"username": tftypes.NewValue(tftypes.String, "administrator"),
		"password": tftypes.NewValue(tftypes.String, "secret"),
		"default_options": tftypes.NewValue(optsType, map[string]tftypes.Value{
			"role": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{
				tftypes.NewValue(tftypes.String, "raw"),
			}),
		}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	data, ok := resp.ResourceData.(*providerData)
	if !ok {
		t.Fatalf("ResourceData is %T, want *providerData", resp.ResourceData)
	}
	if resp.DataSourceData != resp.ResourceData {
		t.Error("DataSourceData must be the same providerData as ResourceData")
	}
	want := map[string][]string{"role": {"raw"}}
	if !reflect.DeepEqual(data.DefaultOptions, want) {
		t.Errorf("DefaultOptions = %v, want %v", data.DefaultOptions, want)
	}
}

func TestProviderConfigure_envFallback(t *testing.T) {
	t.Setenv("MIDPOINT_URL", "http://localhost:8080")
	t.Setenv("MIDPOINT_USERNAME", "administrator")
	t.Setenv("MIDPOINT_PASSWORD", "secret")
	t.Setenv("MIDPOINT_INSECURE", "true")
	resp := configure(t, nil)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if _, ok := resp.ResourceData.(*providerData); !ok {
		t.Fatalf("ResourceData is %T, want *providerData", resp.ResourceData)
	}
}

func TestProviderConfigure_missingCredentials(t *testing.T) {
	clearEnv(t)
	resp := configure(t, nil)
	if got := resp.Diagnostics.ErrorsCount(); got != 3 {
		t.Fatalf("got %d errors, want 3 (url, username, password): %v", got, resp.Diagnostics)
	}
	if resp.ResourceData != nil {
		t.Error("ResourceData must not be set when configuration fails")
	}
}

func TestProviderConfigure_invalidInsecureEnv(t *testing.T) {
	t.Setenv("MIDPOINT_URL", "http://localhost:8080")
	t.Setenv("MIDPOINT_USERNAME", "administrator")
	t.Setenv("MIDPOINT_PASSWORD", "secret")
	t.Setenv("MIDPOINT_INSECURE", "notabool")
	resp := configure(t, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for an invalid MIDPOINT_INSECURE value")
	}
}
