package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource                     = (*objectDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*objectDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*objectDataSource)(nil)
)

// lookupLimit is the maxSize of a data source search: enough to tell one
// match from several, since midPoint 4.9 reports no total count.
const lookupLimit = 2

// objectDataSource is data.midpoint_object: one object of any type, found
// by oid or by name.
type objectDataSource struct {
	data *providerData
}

// objectDataModel maps the data.midpoint_object schema.
type objectDataModel struct {
	ID   types.String  `tfsdk:"id"`
	Type types.String  `tfsdk:"type"`
	OID  types.String  `tfsdk:"oid"`
	Name types.String  `tfsdk:"name"`
	Body types.Dynamic `tfsdk:"body"`
}

// NewObjectDataSource returns the midpoint_object data source.
func NewObjectDataSource() datasource.DataSource {
	return &objectDataSource{}
}

func (d *objectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_object"
}

func (d *objectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one midPoint object of any type by oid or by name, e.g. a built-in archetype, " +
			"so that configurations need not hard-code OIDs.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The object OID.",
				Computed:    true,
			},
			"type": schema.StringAttribute{
				Description: "REST collection type of the object, e.g. archetypes, roles or functionLibraries.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(restTypePattern, "must be a midPoint REST collection type such as roles or functionLibraries"),
				},
			},
			"oid": schema.StringAttribute{
				Description: "OID of the object. Exactly one of oid and name must be set.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "Name of the object; it must match exactly one object of the type. " +
					"Exactly one of oid and name must be set.",
				Optional: true,
				Computed: true,
			},
			"body": schema.DynamicAttribute{
				Description: "The object content in canonical form, without its type wrapper, its oid, " +
					"container ids and server-managed items. Protected strings (passwords and other secrets, " +
					"clear, encrypted or hashed) are removed, with the containers left empty by that.",
				Computed: true,
			},
		},
	}
}

func (d *objectDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("oid"), path.MatchRoot("name")),
	}
}

func (d *objectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if data := configuredDataSourceData(req, resp); data != nil {
		d.data = data
	}
}

func (d *objectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m objectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	typ := m.Type.ValueString()

	oid := m.OID.ValueString()
	if m.OID.IsNull() {
		name := m.Name.ValueString()
		objs, _, err := d.data.Client.Search(ctx, typ, "name = "+axiomString(name), lookupLimit, nil)
		if err != nil {
			resp.Diagnostics.AddError("Cannot look up object", fmt.Sprintf("searching %s named %q: %s", typ, name, err))
			return
		}
		switch len(objs) {
		case 0:
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Object not found",
				fmt.Sprintf("no %s named %q", typ, name))
			return
		case 1:
			oid = objs[0].OID
		default:
			oids := make([]string, len(objs))
			for i, o := range objs {
				oids[i] = o.OID
			}
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous object name",
				fmt.Sprintf("more than one %s named %q (oids %s); set oid instead of name to choose one",
					typ, name, strings.Join(oids, ", ")))
			return
		}
	}

	actual, found, diags := newEngine(d.data, objectSpec(typ)).read(ctx, oid)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddAttributeError(path.Root("oid"), "Object not found",
			fmt.Sprintf("no %s with oid %s", typ, oid))
		return
	}

	body, diags := goToDyn(ctx, stripProtected(stateBody(actual)))
	resp.Diagnostics.Append(diags...)
	m.ID = types.StringValue(oid)
	m.OID = types.StringValue(oid)
	if m.Name.IsNull() {
		m.Name = canonicalName(typ, actual)
	}
	m.Body = body
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
