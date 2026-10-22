package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// focusState is implemented by the pointer to each resource's state struct,
// which embeds focusModel and maps the kind-specific attributes.
type focusState interface {
	model() focusModel
	setModel(focusModel)
}

// newFocusResource returns a role-like resource of kind on the shared typed
// lifecycle. S is the resource's state struct and P its pointer type;
// attributes are the kind-specific schema attributes.
func newFocusResource[S any, P interface {
	*S
	focusState
}](kind focusKind, attributes map[string]schema.Attribute) resource.Resource {
	return &typedResource[S]{
		name:   kind.Name,
		spec:   kind.Spec,
		schema: focusSchema(kind, attributes),
		toBody: func(ctx context.Context, s *S, create, raw bool) (map[string]any, diag.Diagnostics) {
			// Only the create body writes the computed archetype references.
			return focusToBody(ctx, P(s).model(), kind, create && raw)
		},
		fromBody: func(ctx context.Context, s *S, actual map[string]any) diag.Diagnostics {
			m, diags := focusFromBody(ctx, actual, P(s).model(), kind)
			P(s).setModel(m)
			return diags
		},
		// The oid of a target configured by filter only is computed: the
		// oid midPoint resolved the filter to.
		observe: func(s *S, actual map[string]any) {
			m := P(s).model()
			resolveTargetOIDs(kind.Spec.RestType, &m, actual)
			P(s).setModel(m)
		},
		bodyModel: func(ctx context.Context, s *S, config tfsdk.Config) (*S, diag.Diagnostics) {
			m, diags := withoutComputedTargetOIDs(ctx, P(s).model(), config)
			out := *s
			P(&out).setModel(m)
			return &out, diags
		},
		validate: func(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
			// A dynamic block whose for_each is not known yet leaves the
			// whole list unknown; it is validated once known.
			for _, block := range []string{"assignment", "inducement"} {
				var l types.List
				resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(block), &l)...)
				if l.IsUnknown() {
					return
				}
			}
			var s S
			resp.Diagnostics.Append(req.Config.Get(ctx, &s)...)
			if resp.Diagnostics.HasError() {
				return
			}
			resp.Diagnostics.Append(validateFocus(ctx, P(&s).model(), kind)...)
		},
	}
}

// focusSchema is the schema of the role-like resource of kind, with the
// kind-specific attributes.
func focusSchema(kind focusKind, attributes map[string]schema.Attribute) schema.Schema {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Description: desc, Optional: true}
	}
	attrs := map[string]schema.Attribute{
		"name":            schema.StringAttribute{Description: "Object name.", Required: true},
		"display_name":    str("Display name (displayName)."),
		"description":     str("Description."),
		"identifier":      str("Identifier."),
		"lifecycle_state": str("Lifecycle state (lifecycleState), e.g. active, draft, proposed."),
		"risk_level":      str("Risk level (riskLevel)."),
		"requestable":     schema.BoolAttribute{Description: "Whether users can request it.", Optional: true},
		"delegable":       schema.BoolAttribute{Description: "Whether its holders can delegate it.", Optional: true},
		"archetype": str("OID of the archetype, written as an assignment to it. On read that assignment is shown here " +
			"(after an import, or when archetype was set); do not also write it as an assignment block."),
		"extension": schema.DynamicAttribute{
			Description: "Schema extension items, e.g. { costCenter = \"CC-123\" }. Values are scalars or lists. Each item " +
				"must be defined by a midPoint schema extension: midPoint rejects an undefined item on a write that is not " +
				"raw, and a raw write stores it undefined, returned as \"#costCenter\", which never matches the configuration.",
			Optional: true,
		},
		"extra": schema.DynamicAttribute{
			Description: "Other midPoint items, merged at the object root, in midPoint naming (camelCase, as in the " +
				"REST API), e.g. yamldecode(file(\"extra.yaml\")). An item must not also be set by a typed attribute; " +
				"assignment and inducement values here are added to the blocks (they hold the values a block cannot express).",
			Optional: true,
		},
	}
	for k, v := range attributes {
		attrs[k] = v
	}
	return typedSchema(kind.Spec, kind.Description, attrs, map[string]schema.Block{
		"assignment": assignmentBlock("An assignment (membership, archetype, org)."),
		"inducement": assignmentBlock("An inducement: what holders of this object get."),
		"activation": schema.SingleNestedBlock{
			Description: "Activation. Other activation items (validity) go in extra.",
			Attributes: map[string]schema.Attribute{
				"administrative_status": str("Administrative status (administrativeStatus): enabled, disabled or archived."),
			},
		},
	})
}
