package provider

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
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

// versionServer starts a stub midPoint answering GET /nodes/current with
// status and counts the version requests it receives.
func versionServer(t *testing.T, status int) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/midpoint/ws/rest/nodes/current" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		hits.Add(1)
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{"node":{"build":{"version":"4.9.9"}}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

func TestProviderConfigure(t *testing.T) {
	str := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	listType := tftypes.List{ElementType: tftypes.String}
	optsType := tftypes.Map{ElementType: listType}

	attrURL, attrHits := versionServer(t, http.StatusOK)
	envURL, envHits := versionServer(t, http.StatusOK)
	downURL, downHits := versionServer(t, http.StatusInternalServerError)
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsSrv.Close)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw}))

	credsEnv := map[string]string{
		"MIDPOINT_URL":      envURL,
		"MIDPOINT_USERNAME": "env-user",
		"MIDPOINT_PASSWORD": "env-pass",
	}
	withEnv := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range credsEnv {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}

	tests := []struct {
		name         string
		env          map[string]string
		vals         map[string]tftypes.Value
		wantErrs     int
		wantWarnings int
		wantHits     *atomic.Int32 // server that must receive the version request
		wantConfig   providerConfig
		wantDefaults map[string][]string
	}{
		{
			name: "attributes",
			vals: map[string]tftypes.Value{
				"url":       str(attrURL),
				"username":  str("attr-user"),
				"password":  str("attr-pass"),
				"insecure":  tftypes.NewValue(tftypes.Bool, true),
				"ca_bundle": str(caPEM),
				"default_options": tftypes.NewValue(optsType, map[string]tftypes.Value{
					"role": tftypes.NewValue(listType, []tftypes.Value{str("raw")}),
				}),
			},
			wantHits: attrHits,
			wantConfig: providerConfig{
				URL: attrURL, Username: "attr-user", Password: "attr-pass", Insecure: true, CABundle: caPEM,
			},
			wantDefaults: map[string][]string{"role": {"raw"}},
		},
		{
			name: "attributes take precedence over environment",
			env:  withEnv(map[string]string{"MIDPOINT_INSECURE": "true"}),
			vals: map[string]tftypes.Value{
				"url":      str(attrURL),
				"insecure": tftypes.NewValue(tftypes.Bool, false),
			},
			wantHits:   attrHits,
			wantConfig: providerConfig{URL: attrURL, Username: "env-user", Password: "env-pass"},
		},
		{
			name:       "environment fallback",
			env:        withEnv(map[string]string{"MIDPOINT_INSECURE": "true"}),
			wantHits:   envHits,
			wantConfig: providerConfig{URL: envURL, Username: "env-user", Password: "env-pass", Insecure: true},
		},
		{
			name:         "server version failure is a warning",
			env:          withEnv(map[string]string{"MIDPOINT_URL": downURL}),
			wantWarnings: 1,
			wantHits:     downHits,
			wantConfig:   providerConfig{URL: downURL, Username: "env-user", Password: "env-pass"},
		},
		{
			name:     "missing credentials",
			wantErrs: 3,
		},
		{
			name:     "invalid MIDPOINT_INSECURE",
			env:      withEnv(map[string]string{"MIDPOINT_INSECURE": "notabool"}),
			wantErrs: 1,
		},
		{
			name:     "invalid ca_bundle",
			env:      withEnv(nil),
			vals:     map[string]tftypes.Value{"ca_bundle": str("not a PEM certificate")},
			wantErrs: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"MIDPOINT_URL", "MIDPOINT_USERNAME", "MIDPOINT_PASSWORD", "MIDPOINT_INSECURE"} {
				t.Setenv(name, tc.env[name])
			}
			var hitsBefore int32
			if tc.wantHits != nil {
				hitsBefore = tc.wantHits.Load()
			}
			resp := configure(t, tc.vals)

			if got := resp.Diagnostics.ErrorsCount(); got != tc.wantErrs {
				t.Fatalf("got %d errors, want %d: %v", got, tc.wantErrs, resp.Diagnostics)
			}
			if got := resp.Diagnostics.WarningsCount(); got != tc.wantWarnings {
				t.Errorf("got %d warnings, want %d: %v", got, tc.wantWarnings, resp.Diagnostics)
			}
			if tc.wantErrs > 0 {
				if resp.ResourceData != nil {
					t.Error("ResourceData must not be set when configuration fails")
				}
				return
			}
			data, ok := resp.ResourceData.(*providerData)
			if !ok {
				t.Fatalf("ResourceData is %T, want *providerData", resp.ResourceData)
			}
			if resp.DataSourceData != resp.ResourceData {
				t.Error("DataSourceData must be the same providerData as ResourceData")
			}
			if data.Config != tc.wantConfig {
				t.Errorf("Config = %+v, want %+v", data.Config, tc.wantConfig)
			}
			if data.Client == nil {
				t.Error("Client must be set")
			}
			if got := tc.wantHits.Load() - hitsBefore; got != 1 {
				t.Errorf("server received %d version requests, want 1", got)
			}
			wantDefaults := tc.wantDefaults
			if wantDefaults == nil {
				wantDefaults = map[string][]string{}
			}
			if !reflect.DeepEqual(data.DefaultOptions, wantDefaults) {
				t.Errorf("DefaultOptions = %v, want %v", data.DefaultOptions, wantDefaults)
			}
		})
	}
}
