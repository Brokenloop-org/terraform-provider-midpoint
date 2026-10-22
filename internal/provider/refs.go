package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
				Description: "Target object OID.",
				Optional:    true,
			},
			"filter": schema.StringAttribute{
				Description: "midPoint text query selecting the target, e.g. name = \"vault / operator\". " +
					"midPoint resolves it when the object is written; with a filter only, Terraform knows no " +
					"dependency on the target, so ordering is up to the configuration.",
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
			f, isMap := v.(map[string]any)
			if !isMap || len(f) != 1 {
				return t, false
			}
			text, isStr := f["text"].(string)
			if !isStr {
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
