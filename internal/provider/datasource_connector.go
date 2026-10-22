package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

var (
	_ datasource.DataSource              = (*connectorDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*connectorDataSource)(nil)
)

// connectorDataSource is data.midpoint_connector: the connector of a
// connector type and, optionally, version.
type connectorDataSource struct {
	data *providerData
}

// connectorDataModel maps the data.midpoint_connector schema.
type connectorDataModel struct {
	ID               types.String `tfsdk:"id"`
	ConnectorType    types.String `tfsdk:"connector_type"`
	ConnectorVersion types.String `tfsdk:"connector_version"`
	OID              types.String `tfsdk:"oid"`
	Name             types.String `tfsdk:"name"`
}

// NewConnectorDataSource returns the midpoint_connector data source.
func NewConnectorDataSource() datasource.DataSource {
	return &connectorDataSource{}
}

func (d *connectorDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_connector"
}

func (d *connectorDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up the midPoint connector of a connector type (and version), " +
			"so that configurations need not hard-code connector OIDs.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The connector OID.",
				Computed:    true,
			},
			"connector_type": schema.StringAttribute{
				Description: "Connector type, e.g. com.evolveum.polygon.connector.csv.CsvConnector.",
				Required:    true,
			},
			"connector_version": schema.StringAttribute{
				Description: "Connector version, e.g. 2.8. Required when midPoint has several versions of the " +
					"connector type; otherwise read from the connector found.",
				Optional: true,
				Computed: true,
			},
			"oid": schema.StringAttribute{
				Description: "The connector OID.",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The connector name.",
				Computed:    true,
			},
		},
	}
}

func (d *connectorDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if data := configuredDataSourceData(req, resp); data != nil {
		d.data = data
	}
}

func (d *connectorDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m connectorDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	typ, version := m.ConnectorType.ValueString(), m.ConnectorVersion.ValueString()

	objs, ok := d.search(ctx, typ, version, resp)
	if !ok {
		return
	}
	switch len(objs) {
	case 0:
		detail := fmt.Sprintf("no connector of type %s", typ)
		if version != "" {
			detail += " with version " + version
			// List the versions there are, to make the error actionable.
			others, ok := d.search(ctx, typ, "", resp)
			if !ok {
				return
			}
			if len(others) > 0 {
				detail += "; available versions: " + describeConnectors(others)
			}
		}
		resp.Diagnostics.AddError("Connector not found", detail)
		return
	case 1:
	default:
		resp.Diagnostics.AddError("Ambiguous connector",
			fmt.Sprintf("more than one connector matches type %s%s: %s; set connector_version to choose one",
				typ, versionSuffix(version), describeConnectors(objs)))
		return
	}

	c := objs[0]
	m.OID = types.StringValue(c.OID)
	m.ID = m.OID
	m.Name = canonicalName("connectors", c.Body)
	if v, ok := c.Body["connectorVersion"].(string); ok {
		m.ConnectorVersion = types.StringValue(v)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// search finds up to lookupLimit connectors of type typ (and version, when
// set); ok is false when it failed (with an error in resp).
func (d *connectorDataSource) search(ctx context.Context, typ, version string, resp *datasource.ReadResponse) ([]client.Object, bool) {
	objs, _, err := d.data.Client.Search(ctx, "connectors", connectorFilter(typ, version), lookupLimit, nil)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("connector_type"), "Cannot look up connector",
			fmt.Sprintf("searching connectors of type %s%s: %s", typ, versionSuffix(version), err))
		return nil, false
	}
	return objs, true
}

func versionSuffix(version string) string {
	if version == "" {
		return ""
	}
	return " and version " + version
}

// describeConnectors lists connectors as "type version V (oid O)".
func describeConnectors(objs []client.Object) string {
	parts := make([]string, len(objs))
	for i, o := range objs {
		typ, _ := o.Body["connectorType"].(string)
		version, _ := o.Body["connectorVersion"].(string)
		parts[i] = fmt.Sprintf("%s version %s (oid %s)", typ, version, o.OID)
	}
	return strings.Join(parts, ", ")
}
