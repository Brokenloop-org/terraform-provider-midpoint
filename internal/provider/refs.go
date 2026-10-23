package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// defaultRelation is the relation midPoint gives a reference that has none.
const defaultRelation = "org:default"

// targetRef maps a target block: a reference by oid, by midPoint text
// filter, or both.
type targetRef struct {
	OID      types.String `tfsdk:"oid"`
	Filter   types.String `tfsdk:"filter"`
	Type     types.String `tfsdk:"type"`
	Relation types.String `tfsdk:"relation"`
}

// targetBlock is the schema of a target block.
func targetBlock() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		Description: "The assigned or induced object, by oid, by filter, or both. At least one of oid and filter is required.",
		Validators:  []validator.Object{targetValidator{}},
		Attributes: map[string]schema.Attribute{
			"oid": schema.StringAttribute{
				Description: "Target object OID. With a filter and no oid configured, this is computed: the OID " +
					"midPoint resolved the filter to, recorded on create, update, refresh and import. It is kept while " +
					"filter, type and relation stay the same, and it is never sent to midPoint (midPoint keeps both " +
					"the filter and the resolved OID on the reference).",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{targetOIDPlanModifier{}},
			},
			"filter": schema.StringAttribute{
				Description: "midPoint text query selecting the target, e.g. name = \"vault / operator\". " +
					"midPoint resolves it when the object is written; with a filter only, Terraform knows no " +
					"dependency on the target, so ordering is up to the configuration. A structured filter stored in " +
					"midPoint (equal conditions joined by and, or, not) is read as its text rendering, with the " +
					"operands of and/or sorted; when that rendering equals this value the stored filter counts as " +
					"equal and is never rewritten, also when another item of the object changes. A stored filter " +
					"that has no text rendering is not read into this block: the item holding the reference stays in extra.",
				Optional: true,
			},
			"type": schema.StringAttribute{
				Description: "Target object type, e.g. RoleType, ServiceType, OrgType, ArchetypeType.",
				Optional:    true,
			},
			"relation": schema.StringAttribute{
				Description: "Relation of the reference, e.g. org:manager. Defaults to org:default. A name without a " +
					"prefix is in the org: namespace, as midPoint stores it: manager is written org:manager.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString(defaultRelation),
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
		},
	}
}

// qualifiedRelation is the relation midPoint stores for s: a name without a
// namespace prefix is in the org: namespace ("manager" is "org:manager").
func qualifiedRelation(s string) string {
	if strings.Contains(s, ":") {
		return s
	}
	return "org:" + s
}

// refToJSON is the midPoint reference for t; the relation is qualified
// (qualifiedRelation) and the default relation is left out, so a
// configured and a server reference compare alike.
func refToJSON(t targetRef) map[string]any {
	out := map[string]any{}
	if s, ok := knownValue(t.OID); ok {
		out["oid"] = s
	}
	if s, ok := knownValue(t.Type); ok {
		out["type"] = s
	}
	if s, ok := knownValue(t.Relation); ok && qualifiedRelation(s) != defaultRelation {
		out["relation"] = qualifiedRelation(s)
	}
	if s, ok := knownValue(t.Filter); ok {
		out["filter"] = map[string]any{"text": s}
	}
	return out
}

// refFromJSON is the target block for a canonical reference m. ok is false
// when m holds anything a target block cannot express: an item other than
// oid, type, relation and filter, a filter that is not a text filter, or
// neither an oid nor a filter.
func refFromJSON(m map[string]any) (t targetRef, ok bool) {
	t = targetRef{OID: types.StringNull(), Filter: types.StringNull(), Type: types.StringNull(), Relation: types.StringValue(defaultRelation)}
	for k, v := range m {
		switch k {
		case "oid":
			s, isStr := v.(string)
			if !isStr {
				return t, false
			}
			t.OID = types.StringValue(s)
		case "type":
			s, isStr := v.(string)
			if !isStr {
				return t, false
			}
			t.Type = types.StringValue(s)
		case "relation":
			s, isStr := v.(string)
			if !isStr {
				return t, false
			}
			t.Relation = types.StringValue(s)
		case "filter":
			// A structured filter midPoint stored is read as its text
			// rendering (normalize.FilterText, the one the converter
			// writes); any other filter is not expressible.
			f, isMap := v.(map[string]any)
			if !isMap {
				return t, false
			}
			text, textOK := normalize.FilterText(f)
			if !textOK {
				return t, false
			}
			t.Filter = types.StringValue(text)
		default:
			return t, false
		}
	}
	return t, !t.OID.IsNull() || !t.Filter.IsNull()
}

// isArchetypeType reports whether a reference type names ArchetypeType.
func isArchetypeType(typ string) bool {
	return strings.TrimPrefix(typ, "c:") == "ArchetypeType"
}

// targetOIDPlanModifier plans the computed oid of a target configured by
// filter only: the oid recorded in state for a prior target with the same
// filter, type and relation (the same index first, then any other, so that
// reordered blocks keep their oids), else unknown until midPoint resolves
// the filter.
type targetOIDPlanModifier struct{}

func (targetOIDPlanModifier) Description(context.Context) string {
	return "Keeps the OID midPoint resolved for a filter while filter, type and relation are unchanged."
}

func (m targetOIDPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (targetOIDPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() || !req.PlanValue.IsUnknown() || req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	targetPath := req.Path.ParentPath()  // <item>[i].target
	blockPath := targetPath.ParentPath() // <item>[i]
	var planned types.Object
	var prior types.List
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, targetPath, &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, blockPath.ParentPath(), &prior)...)
	if resp.Diagnostics.HasError() || planned.IsNull() || planned.IsUnknown() || prior.IsNull() || prior.IsUnknown() {
		return
	}
	want := planned.Attributes()
	if f := want["filter"]; f == nil || f.IsNull() || f.IsUnknown() {
		return
	}
	candidates := prior.Elements()
	if step, _ := blockPath.Steps().LastStep(); step != nil {
		if i, isIndex := step.(path.PathStepElementKeyInt); isIndex && i >= 0 && int(i) < len(candidates) {
			candidates = append([]attr.Value{candidates[i]}, candidates...)
		}
	}
	for _, c := range candidates {
		block, ok := c.(types.Object)
		if !ok || block.IsNull() || block.IsUnknown() {
			continue
		}
		t, ok := block.Attributes()["target"].(types.Object)
		if !ok || t.IsNull() || t.IsUnknown() {
			continue
		}
		have := t.Attributes()
		oid, ok := have["oid"].(types.String)
		if !ok || oid.IsNull() || oid.IsUnknown() {
			continue
		}
		same := true
		for _, name := range []string{"filter", "type", "relation"} {
			if want[name] == nil || have[name] == nil || !want[name].Equal(have[name]) {
				same = false
			}
		}
		if same {
			resp.PlanValue = oid
			return
		}
	}
}

// knownValue returns the value of a known, non-null string.
func knownValue(s types.String) (string, bool) {
	if s.IsNull() || s.IsUnknown() {
		return "", false
	}
	return s.ValueString(), true
}

// targetValidator requires oid or filter in a present target block.
type targetValidator struct{}

func (targetValidator) Description(context.Context) string {
	return "at least one of oid and filter must be set"
}

func (v targetValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (targetValidator) ValidateObject(ctx context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	attrs := req.ConfigValue.Attributes()
	oid, filter := attrs["oid"], attrs["filter"]
	if oid != nil && filter != nil && oid.IsNull() && filter.IsNull() {
		resp.Diagnostics.AddAttributeError(req.Path, "Incomplete target",
			"a target block needs oid, filter or both")
	}
}
