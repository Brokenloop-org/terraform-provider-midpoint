// Package provider implements the Terraform provider for Evolveum midPoint.
package provider

import (
	"context"
	"net/url"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

// resourceShortNames are the keys accepted in default_options.
var resourceShortNames = []string{
	"service", "role", "org", "archetype", "object_template", "mark", "task", "resource", "object",
}

var _ provider.Provider = (*midpointProvider)(nil)

type midpointProvider struct {
	version string
}

// providerModel maps the provider configuration block.
type providerModel struct {
	URL            types.String `tfsdk:"url"`
	Username       types.String `tfsdk:"username"`
	Password       types.String `tfsdk:"password"`
	Insecure       types.Bool   `tfsdk:"insecure"`
	CABundle       types.String `tfsdk:"ca_bundle"`
	DefaultOptions types.Map    `tfsdk:"default_options"`
}

// providerConfig is the provider configuration after environment fallbacks.
type providerConfig struct {
	URL      string
	Username string
	Password string
	Insecure bool
	CABundle string
}

// providerData is handed to resources and data sources via Configure.
type providerData struct {
	Client         *client.Client
	Config         providerConfig
	DefaultOptions map[string][]string
}

// New returns a constructor for the midPoint provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &midpointProvider{version: version}
	}
}

func (p *midpointProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "midpoint"
	resp.Version = p.version
}

func (p *midpointProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages Evolveum midPoint objects through the midPoint REST API.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Description: "Base URL of the midPoint server, e.g. https://midpoint.example.com. May also be set with MIDPOINT_URL.",
				Optional:    true,
			},
			"username": schema.StringAttribute{
				Description: "Username for HTTP basic authentication. May also be set with MIDPOINT_USERNAME.",
				Optional:    true,
			},
			"password": schema.StringAttribute{
				Description: "Password for HTTP basic authentication. May also be set with MIDPOINT_PASSWORD.",
				Optional:    true,
				Sensitive:   true,
			},
			"insecure": schema.BoolAttribute{
				Description: "Skip TLS certificate verification (development only). May also be set with MIDPOINT_INSECURE. Defaults to false.",
				Optional:    true,
			},
			"ca_bundle": schema.StringAttribute{
				Description: "PEM-encoded CA certificates used to verify the midPoint server certificate.",
				Optional:    true,
			},
			"default_options": schema.MapAttribute{
				Description: "Default midPoint execution options per resource type, keyed by resource short name " +
					"(service, role, org, archetype, object_template, mark, task, resource, object). " +
					"A resource's own options attribute takes precedence.",
				Optional:    true,
				ElementType: types.ListType{ElemType: types.StringType},
				Validators: []validator.Map{
					mapvalidator.KeysAre(stringvalidator.OneOf(resourceShortNames...)),
				},
			},
		},
	}
}

func (p *midpointProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var model providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg, diags := resolveConfig(model)
	resp.Diagnostics.Append(diags...)

	defaults := map[string][]string{}
	if !model.DefaultOptions.IsNull() && !model.DefaultOptions.IsUnknown() {
		resp.Diagnostics.Append(model.DefaultOptions.ElementsAs(ctx, &defaults, false)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "configured midPoint provider", map[string]any{
		"url":      redactURL(cfg.URL),
		"username": cfg.Username,
		"insecure": cfg.Insecure,
	})

	var caBundle []byte
	if cfg.CABundle != "" {
		caBundle = []byte(cfg.CABundle)
	}
	c, err := client.New(client.Config{
		URL:       cfg.URL,
		Username:  cfg.Username,
		Password:  cfg.Password,
		Insecure:  cfg.Insecure,
		CABundle:  caBundle,
		UserAgent: "terraform-provider-midpoint/" + p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create midPoint client", err.Error())
		return
	}

	if version, err := c.ServerVersion(ctx); err != nil {
		resp.Diagnostics.AddWarning("Unable to read midPoint server version",
			"Reading the server version from "+redactURL(cfg.URL)+" failed; later requests may fail too: "+err.Error())
	} else {
		tflog.Info(ctx, "connected to midPoint", map[string]any{"url": redactURL(cfg.URL), "version": version})
	}

	data := &providerData{Client: c, Config: cfg, DefaultOptions: defaults}
	resp.ResourceData = data
	resp.DataSourceData = data
}

// resolveConfig applies environment fallbacks and validates the result.
func resolveConfig(model providerModel) (providerConfig, diag.Diagnostics) {
	var diags diag.Diagnostics
	var cfg providerConfig

	cfg.URL = stringOrEnv(model.URL, "MIDPOINT_URL", "url", &diags)
	cfg.Username = stringOrEnv(model.Username, "MIDPOINT_USERNAME", "username", &diags)
	cfg.Password = stringOrEnv(model.Password, "MIDPOINT_PASSWORD", "password", &diags)

	switch {
	case model.Insecure.IsUnknown():
		diags.AddAttributeError(path.Root("insecure"), "Unknown midPoint insecure flag",
			"The provider cannot be configured with a value that is unknown until apply.")
	case !model.Insecure.IsNull():
		cfg.Insecure = model.Insecure.ValueBool()
	default:
		if env := os.Getenv("MIDPOINT_INSECURE"); env != "" {
			v, err := strconv.ParseBool(env)
			if err != nil {
				diags.AddAttributeError(path.Root("insecure"), "Invalid MIDPOINT_INSECURE",
					"MIDPOINT_INSECURE must be a boolean (true or false): "+err.Error())
			}
			cfg.Insecure = v
		}
	}

	if model.CABundle.IsUnknown() {
		diags.AddAttributeError(path.Root("ca_bundle"), "Unknown midPoint CA bundle",
			"The provider cannot be configured with a value that is unknown until apply.")
	}
	cfg.CABundle = model.CABundle.ValueString()

	return cfg, diags
}

// redactURL returns s with any password masked, for logs and diagnostics.
func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "(invalid URL)"
	}
	return u.Redacted()
}

// stringOrEnv returns the attribute value, falling back to the environment
// variable, and records an error when neither provides a value.
func stringOrEnv(v types.String, env, attr string, diags *diag.Diagnostics) string {
	if v.IsUnknown() {
		diags.AddAttributeError(path.Root(attr), "Unknown midPoint "+attr,
			"The provider cannot be configured with a value that is unknown until apply. "+
				"Set the value statically or use the "+env+" environment variable.")
		return ""
	}
	s := v.ValueString()
	if v.IsNull() {
		s = os.Getenv(env)
	}
	if s == "" {
		diags.AddAttributeError(path.Root(attr), "Missing midPoint "+attr,
			"Set the "+attr+" attribute in the provider configuration or the "+env+" environment variable.")
	}
	return s
}

func (p *midpointProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewObjectResource,
	}
}

func (p *midpointProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
