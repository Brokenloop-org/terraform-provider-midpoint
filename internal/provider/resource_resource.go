package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

var resourceSpec = engineSpec{
	RestType: "resources", ShortName: "resource",
	CreateOptions: []string{"isImport", "reevaluateSearchFilters"},
	UpdateOptions: []string{"reevaluateSearchFilters"},
	ReadOptions:   []string{"noFetch"},
	MembersCheck:  shadowsCheck,
}

// xmlSchemaNamespace is the "@ns" of a resource schema definition; midPoint
// cannot parse the definition without it.
const xmlSchemaNamespace = "http://www.w3.org/2001/XMLSchema"

// connectorModel maps the connector attribute.
type connectorModel struct {
	Type    types.String `tfsdk:"type"`
	Version types.String `tfsdk:"version"`
}

var connectorAttrTypes = map[string]attr.Type{"type": types.StringType, "version": types.StringType}

// resourceSchemaModel maps the schema attribute.
type resourceSchemaModel struct {
	GenerationConstraints types.Dynamic `tfsdk:"generation_constraints"`
	Definition            types.String  `tfsdk:"definition"`
}

var resourceSchemaAttrTypes = map[string]attr.Type{"generation_constraints": types.DynamicType, "definition": types.StringType}

// resourceState maps the midpoint_resource schema.
type resourceState struct {
	commonModel
	Name                   types.String  `tfsdk:"name"`
	Description            types.String  `tfsdk:"description"`
	LifecycleState         types.String  `tfsdk:"lifecycle_state"`
	Connector              types.Object  `tfsdk:"connector"`
	ConnectorOID           types.String  `tfsdk:"connector_oid"`
	ConnectorConfiguration types.Dynamic `tfsdk:"connector_configuration"`
	SchemaHandling         types.Dynamic `tfsdk:"schema_handling"`
	Synchronization        types.Dynamic `tfsdk:"synchronization"`
	Capabilities           types.Dynamic `tfsdk:"capabilities"`
	Schema                 types.Object  `tfsdk:"schema"`
	Extra                  types.Dynamic `tfsdk:"extra"`
}

func (s *resourceState) fields() simpleFields {
	return simpleFields{
		Strings: []stringField{
			{"name", "name", &s.Name},
			{"description", "description", &s.Description},
			{"lifecycleState", "lifecycle_state", &s.LifecycleState},
		},
		Dynamics: []dynamicField{
			{"connectorConfiguration", "connector_configuration", &s.ConnectorConfiguration},
			{"schemaHandling", "schema_handling", &s.SchemaHandling},
			{"synchronization", "synchronization", &s.Synchronization},
		},
		Extra: &s.Extra,
		Reserved: map[string]string{
			"connectorRef": "connector",
			"capabilities": "capabilities",
			"schema":       "schema",
		},
	}
}

// connector returns the connector attribute; ok is false when it is null
// or unknown (an imported resource has none until it is configured).
func (s *resourceState) connector(ctx context.Context) (m connectorModel, ok bool, diags diag.Diagnostics) {
	if s.Connector.IsNull() || s.Connector.IsUnknown() {
		return m, false, diags
	}
	diags = s.Connector.As(ctx, &m, basetypes.ObjectAsOptions{})
	return m, !diags.HasError(), diags
}

// schemaModel returns the schema attribute; ok is false when it is null or
// unknown.
func (s *resourceState) schemaModel(ctx context.Context) (m resourceSchemaModel, ok bool, diags diag.Diagnostics) {
	if s.Schema.IsNull() || s.Schema.IsUnknown() {
		return m, false, diags
	}
	diags = s.Schema.As(ctx, &m, basetypes.ObjectAsOptions{})
	return m, !diags.HasError(), diags
}

// connectorRefValue is the connectorRef that finds the connector of type
// typ (and version, when set) by a search filter, so the configuration
// holds no environment-specific connector OID.
func connectorRefValue(typ, version string) map[string]any {
	return map[string]any{"type": "ConnectorType", "filter": map[string]any{"text": connectorFilter(typ, version)}}
}

// connectorFilter is the Axiom filter for the connectors of type typ and,
// when it is set, version.
func connectorFilter(typ, version string) string {
	text := "connectorType = " + axiomString(typ)
	if version != "" {
		text += " and connectorVersion = " + axiomString(version)
	}
	return text
}

// resourceToBody is the body of s: the simple fields, connectorRef from
// connector, capabilities/configured from capabilities and schema from
// schema. The definition is written trimmed, with the XML Schema "@ns"
// midPoint needs to parse it.
func resourceToBody(ctx context.Context, s *resourceState) (map[string]any, diag.Diagnostics) {
	body, diags := simpleToBody(ctx, s.fields())
	c, ok, d := s.connector(ctx)
	diags.Append(d...)
	if typ, known := knownValue(c.Type); ok && known {
		version, _ := knownValue(c.Version)
		body["connectorRef"] = connectorRefValue(typ, version)
	}
	caps, d := dynToGo(ctx, s.Capabilities)
	diags.Append(d...)
	if caps != nil {
		body["capabilities"] = map[string]any{"configured": caps}
	}
	sm, ok, d := s.schemaModel(ctx)
	diags.Append(d...)
	if ok {
		schemaBody := map[string]any{}
		gc, d := dynToGo(ctx, sm.GenerationConstraints)
		diags.Append(d...)
		if gc != nil {
			schemaBody["generationConstraints"] = gc
		}
		if def, known := knownValue(sm.Definition); known && strings.TrimSpace(def) != "" {
			schemaBody["definition"] = map[string]any{"@ns": xmlSchemaNamespace, "schema": strings.TrimSpace(def)}
		}
		if len(schemaBody) > 0 {
			body["schema"] = schemaBody
		}
	}
	return body, diags
}

// resourceFromBody sets s from actual. connector is kept from the prior
// state while midPoint holds the configured connectorRef (its resolved oid
// is connector_oid, not compared); a different connectorRef reads back as
// a null connector, so the plan restores the configured one.
// capabilities is read from capabilities/configured (native capabilities
// are server-managed), schema from schema (a cached definition is compared
// only when one is configured; a definition equal to the configured one
// after trimming keeps the configured text).
func resourceFromBody(ctx context.Context, s *resourceState, actual map[string]any) diag.Diagnostics {
	prior := *s
	desired, diags := resourceToBody(ctx, &prior)
	canonicalDesired := normalize.Canonical(resourceSpec.RestType, pruneEmpty(desired))
	priorSchema, _, d := prior.schemaModel(ctx)
	diags.Append(d...)
	diags.Append(simpleFromBody(ctx, resourceSpec.RestType, desired, actual, prior.fields(), s.fields(), func(rest map[string]any) {
		if ref, ok := canonicalDesired["connectorRef"]; !ok || !normalize.Equal(ref, rest["connectorRef"]) {
			s.Connector = types.ObjectNull(connectorAttrTypes)
		}
		delete(rest, "connectorRef")

		s.Capabilities = types.DynamicNull()
		if caps, ok := rest["capabilities"].(map[string]any); ok && caps["configured"] != nil {
			var d diag.Diagnostics
			s.Capabilities, d = keepOrConvertAt(ctx, resourceSpec.RestType, []string{"capabilities", "configured"}, prior.Capabilities, caps["configured"])
			diags.Append(d...)
		}
		delete(rest, "capabilities")

		s.Schema = types.ObjectNull(resourceSchemaAttrTypes)
		sch, _ := rest["schema"].(map[string]any)
		delete(rest, "schema")
		m := resourceSchemaModel{GenerationConstraints: types.DynamicNull(), Definition: types.StringNull()}
		if gc := sch["generationConstraints"]; gc != nil {
			var d diag.Diagnostics
			m.GenerationConstraints, d = keepOrConvertAt(ctx, resourceSpec.RestType, []string{"schema", "generationConstraints"}, priorSchema.GenerationConstraints, gc)
			diags.Append(d...)
		}
		if def, ok := sch["definition"].(map[string]any); ok {
			if text, ok := def["schema"].(string); ok {
				m.Definition = types.StringValue(text)
				if p, known := knownValue(priorSchema.Definition); known && strings.TrimSpace(p) == strings.TrimSpace(text) {
					m.Definition = priorSchema.Definition
				}
			}
		}
		if !m.GenerationConstraints.IsNull() || !m.Definition.IsNull() {
			var d diag.Diagnostics
			s.Schema, d = types.ObjectValueFrom(ctx, resourceSchemaAttrTypes, m)
			diags.Append(d...)
		}
	})...)
	return diags
}

// validateResource runs the simple-resource checks and warns when the
// connector configuration holds a clear-text secret.
func validateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	simpleValidator(resourceSpec.ShortName, (*resourceState).fields)(ctx, req, resp)
	var cc types.Dynamic
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("connector_configuration"), &cc)...)
	if cc.IsNull() || cc.IsUnknown() {
		return
	}
	// Unknown parts convert with an error; only known content is checked.
	if v, d := dynToGo(ctx, cc); !d.HasError() && hasKey(v, "clearValue") {
		resp.Diagnostics.AddAttributeWarning(path.Root("connector_configuration"), "Clear-text secret in configuration",
			"connector_configuration contains a clearValue; it will be stored in Terraform state in plain text. "+
				"Prefer expression.const references or midPoint secret providers.")
	}
}

// hasKey reports whether a map anywhere in v has key.
func hasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t[key]; ok {
			return true
		}
		for _, e := range t {
			if hasKey(e, key) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasKey(e, key) {
				return true
			}
		}
	}
	return false
}

// observeConnectorOID sets connector_oid from the connectorRef oid midPoint
// resolved (null before a read).
func observeConnectorOID(s *resourceState, actual map[string]any) {
	s.ConnectorOID = types.StringNull()
	if ref, ok := actual["connectorRef"].(map[string]any); ok {
		if oid, ok := ref["oid"].(string); ok && oid != "" {
			s.ConnectorOID = types.StringValue(oid)
		}
	}
}

// connectorOIDPlanModifier keeps connector_oid from the state while the
// connector is unchanged; a changed connector leaves it unknown until
// midPoint resolves the new filter.
type connectorOIDPlanModifier struct{}

func (connectorOIDPlanModifier) Description(context.Context) string {
	return "Keeps the resolved connector OID while the connector is unchanged."
}

func (m connectorOIDPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (connectorOIDPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	var planned, prior types.Object
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("connector"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("connector"), &prior)...)
	if !resp.Diagnostics.HasError() && planned.Equal(prior) {
		resp.PlanValue = req.StateValue
	}
}

// NewResourceResource returns the midpoint_resource resource.
func NewResourceResource() resource.Resource {
	return &typedResource[resourceState]{
		name: resourceSpec.ShortName,
		spec: resourceSpec,
		schema: typedSchema(resourceSpec,
			"A midPoint resource (ResourceType): a connector, its configuration, schema handling and "+
				"synchronization. The connector is found by type (and version) with a search filter, so the "+
				"configuration holds no environment-specific connector OID. midPoint manages the native "+
				"capabilities, the cached resource schema and the operational state; they are never compared. "+
				"Resources are read with noFetch (no connector call), and a resource with shadows is not "+
				"deleted unless force_destroy is set.",
			map[string]schema.Attribute{
				"name":            nameAttribute(),
				"description":     optionalString("Description."),
				"lifecycle_state": optionalString("Lifecycle state (lifecycleState), e.g. active, proposed."),
				"connector": schema.SingleNestedAttribute{
					Description: "The connector, found by a search filter on connectorType (and connectorVersion when " +
						"version is set). midPoint resolves the filter when it creates or updates the resource; the " +
						"OID it finds is connector_oid. An imported resource reads back without it until it is " +
						"configured, so a config sets it with one no-op update after the import.",
					Required: true,
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Description: "Connector type (connectorType), e.g. com.evolveum.polygon.connector.csv.CsvConnector.",
							Required:    true,
							Validators:  []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"version": schema.StringAttribute{
							Description: "Connector version (connectorVersion), e.g. 2.8. When unset any version " +
								"matches, which fails while several versions of the connector are installed.",
							Optional:   true,
							Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
						},
					},
				},
				"connector_oid": schema.StringAttribute{
					Description: "OID of the connector midPoint resolved from connector. It differs per " +
						"environment and is never compared.",
					Computed:      true,
					PlanModifiers: []planmodifier.String{connectorOIDPlanModifier{}},
				},
				"connector_configuration": optionalDynamic("Connector configuration (connectorConfiguration) in " +
					"midPoint naming, e.g. yamldecode(file(\"connector.yaml\")). The configurationProperties must " +
					"carry the connector's namespace as \"@ns\" (e.g. \"@ns\" = \"http://midpoint.evolveum.com/xml/ns/" +
					"public/connector/icf-1/bundle/com.evolveum.polygon.connector-csv/com.evolveum.polygon." +
					"connector.csv.CsvConnector\"), or midPoint stores them unqualified and the connector finds none. " +
					"\"@ns\" is an annotation, never compared: a change to \"@ns\" alone is not detected, and the " +
					"changes the provider sends are built from the compared form, without \"@ns\". Changes to single " +
					"properties are not affected, but an update that adds connectorConfiguration or " +
					"configurationProperties whole (adopting or importing a resource that has none, or restoring them " +
					"after they were removed in midPoint) writes the properties without their namespace, and the " +
					"connector then finds no configuration; set them in midPoint first in that case. The value is " +
					"stored in Terraform state in plain text: a password given as a clearValue is there too (a warning " +
					"says so); prefer expression const references or midPoint secret providers."),
				"schema_handling": optionalDynamic("Schema handling (schemaHandling) in midPoint naming: object " +
					"types with their attributes and mappings, e.g. yamldecode(file(\"schema-handling.yaml\")). A " +
					"change is applied as narrow item deltas (an added mapping is one add)."),
				"synchronization": optionalDynamic("Synchronization (synchronization) in midPoint naming."),
				"capabilities": optionalDynamic("Configured capabilities (capabilities/configured) in midPoint naming, " +
					"e.g. { create = { enabled = false } }. The native capabilities the connector reports are " +
					"server-managed and never compared."),
				"schema": schema.SingleNestedAttribute{
					Description: "Resource schema settings (schema). midPoint caches the schema it fetches from the " +
						"connector in schema/definition; that cached definition is compared only when definition is " +
						"configured. So removing definition from the configuration leaves the schema in midPoint as it " +
						"is (it is no longer compared); it is not deleted. Configuring or removing generation_constraints " +
						"changes only schema/generationConstraints.",
					Optional: true,
					Attributes: map[string]schema.Attribute{
						"generation_constraints": optionalDynamic("Schema generation constraints " +
							"(generationConstraints) in midPoint naming, e.g. { generateObjectClass = " +
							"[\"ri:AccountObjectClass\"] }."),
						"definition": optionalString("A static resource schema (schema/definition): the xsd:schema " +
							"XML text. Compared after trimming surrounding whitespace. Write it on one line, as " +
							"midPoint returns it: midPoint adds xml:space=\"preserve\" to the root element of an " +
							"indented schema, which then always differs from the configured text."),
					},
				},
				"extra": extraAttribute(),
			}, nil),
		toBody: func(ctx context.Context, s *resourceState, _, _ bool) (map[string]any, diag.Diagnostics) {
			return resourceToBody(ctx, s)
		},
		fromBody: resourceFromBody,
		validate: validateResource,
		observe:  observeConnectorOID,
	}
}
