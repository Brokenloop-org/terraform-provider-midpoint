package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/jplana/terraform-provider-midpoint/internal/delta"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// The simple typed resources (object template, mark, task) map a few root
// items to typed string attributes and a few to free-form dynamic
// attributes; everything else is in extra.

// stringField maps the string attribute Attr to the root item Item.
type stringField struct {
	Item, Attr string
	V          *types.String
}

// dynamicField maps the dynamic attribute Attr to the root item Item. A
// multi-valued item (normalize.MultiValued) accepts a single value or a
// list and is always written as a list.
type dynamicField struct {
	Item, Attr string
	V          *types.Dynamic
}

// simpleFields are the typed attributes of one simple resource state, as
// pointers into it.
type simpleFields struct {
	Strings  []stringField
	Dynamics []dynamicField
	Extra    *types.Dynamic
	// Reserved are further root items the resource writes from other
	// attributes (item -> attribute), e.g. a task's ownerRef from owner.
	// extra must not hold them either.
	Reserved map[string]string
}

// simpleToBody is the body of the typed attributes and extra. It never
// holds the oid.
func simpleToBody(ctx context.Context, f simpleFields) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{}
	for _, s := range f.Strings {
		if v, ok := knownValue(*s.V); ok {
			body[s.Item] = v
		}
	}
	for _, d := range f.Dynamics {
		g, dd := dynToGo(ctx, *d.V)
		diags.Append(dd...)
		if g == nil {
			continue
		}
		if normalize.MultiValued[d.Item] {
			g = asList(g)
		}
		body[d.Item] = g
	}

	extra, d := dynToGo(ctx, *f.Extra)
	diags.Append(d...)
	if extra == nil {
		return body, diags
	}
	em, ok := extra.(map[string]any)
	if !ok {
		diags.AddAttributeError(path.Root("extra"), "Invalid extra", "extra must be an object of midPoint items")
		return body, diags
	}
	for k, v := range em {
		switch {
		case v == nil:
		case k == "oid":
			diags.AddAttributeError(path.Root("extra"), "Conflicting oid", "extra must not hold oid; use the oid attribute")
		case body[k] != nil || f.Reserved[k] != "":
			diags.AddAttributeError(path.Root("extra"), "Conflicting item",
				fmt.Sprintf("extra holds %q, which a typed attribute of this resource already sets; set it in one place", k))
		default:
			body[k] = v
		}
	}
	return body, diags
}

// simpleFromBody sets the attributes of out from actual, the canonical body
// midPoint returned, given desired, the body of the prior state (prior).
// actual is first aligned with desired the way the engine compares them,
// so equal values keep their configured shape. take, when set, runs on the
// aligned body first and removes the items it claims. Each remaining item
// goes to its typed attribute when the attribute can express it (a string
// for a string attribute), else to extra; nothing is dropped.
func simpleFromBody(ctx context.Context, restType string, desired, actual map[string]any, prior, out simpleFields,
	take func(rest map[string]any)) diag.Diagnostics {
	var diags diag.Diagnostics
	_, aligned := delta.AlignForType(restType, normalize.Canonical(restType, pruneEmpty(desired)), actual)
	rest := stateBody(aligned)
	if take != nil {
		take(rest)
	}
	for _, s := range out.Strings {
		*s.V = types.StringNull()
		if v, ok := rest[s.Item].(string); ok {
			*s.V = types.StringValue(v)
			delete(rest, s.Item)
		}
	}
	for i, dst := range out.Dynamics {
		*dst.V = types.DynamicNull()
		v, present := rest[dst.Item]
		if !present {
			continue
		}
		delete(rest, dst.Item)
		var d diag.Diagnostics
		*dst.V, d = keepOrConvert(ctx, restType, dst.Item, *prior.Dynamics[i].V, v)
		diags.Append(d...)
	}
	*out.Extra = types.DynamicNull()
	if len(rest) > 0 {
		var d diag.Diagnostics
		*out.Extra, d = keepOrConvert(ctx, restType, "", *prior.Extra, rest)
		diags.Append(d...)
	}
	return diags
}

// simpleValidate reports extra items that belong to a typed attribute: a
// dynamic attribute's item or a reserved item with any value, and a string
// attribute's item with a string value (a read would move it out of extra,
// so the plan would never converge). A value a string attribute cannot
// express stays allowed in extra.
func simpleValidate(f simpleFields, resourceName string) diag.Diagnostics {
	var diags diag.Diagnostics
	extra := *f.Extra
	if extra.IsNull() || extra.IsUnknown() || extra.IsUnderlyingValueUnknown() {
		return diags
	}
	attrs, ok := bodyAttributes(extra)
	if !ok {
		diags.AddAttributeError(path.Root("extra"), "Invalid extra", "extra must be an object of midPoint items")
		return diags
	}
	typed := map[string]string{}
	for item, a := range f.Reserved {
		typed[item] = a
	}
	for _, d := range f.Dynamics {
		typed[d.Item] = d.Attr
	}
	strs := map[string]string{}
	for _, s := range f.Strings {
		strs[s.Item] = s.Attr
	}
	for k, v := range attrs {
		if v.IsNull() {
			continue
		}
		if k == "oid" {
			diags.AddAttributeError(path.Root("extra"), "Conflicting oid", "extra must not hold oid; use the oid attribute")
			continue
		}
		name, isTyped := typed[k]
		if a, ok := strs[k]; ok {
			if dv, isDyn := v.(basetypes.DynamicValue); isDyn {
				v = dv.UnderlyingValue()
			}
			_, isStr := v.(basetypes.StringValue)
			name, isTyped = a, isStr || v.IsUnknown()
		}
		if isTyped {
			diags.AddAttributeError(path.Root("extra"), "Typed item in extra",
				fmt.Sprintf("%s is a typed attribute of midpoint_%s (%s); set it there instead of in extra", k, resourceName, name))
		}
	}
	return diags
}

// simpleValidator is a typedResource validate func for state S whose
// fields are fields.
func simpleValidator[S any](resourceName string, fields func(*S) simpleFields) func(context.Context, resource.ValidateConfigRequest, *resource.ValidateConfigResponse) {
	return func(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
		var s S
		resp.Diagnostics.Append(req.Config.Get(ctx, &s)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(simpleValidate(fields(&s), resourceName)...)
	}
}

// simpleResource returns a simple typed resource whose state S maps
// fields; extra conversion (beyond the fields) is none.
func simpleResource[S any](name string, spec engineSpec, sch schema.Schema, fields func(*S) simpleFields) *typedResource[S] {
	return &typedResource[S]{
		name:   name,
		spec:   spec,
		schema: sch,
		toBody: func(ctx context.Context, s *S, _, _ bool) (map[string]any, diag.Diagnostics) {
			return simpleToBody(ctx, fields(s))
		},
		fromBody: func(ctx context.Context, s *S, actual map[string]any) diag.Diagnostics {
			prior := *s
			desired, _ := simpleToBody(ctx, fields(&prior))
			return simpleFromBody(ctx, spec.RestType, desired, actual, fields(&prior), fields(s), nil)
		},
		validate: simpleValidator(name, fields),
	}
}

// Shared attribute definitions of the simple resources.

func nameAttribute() schema.Attribute {
	return schema.StringAttribute{Description: "Object name.", Required: true}
}

func optionalString(desc string) schema.Attribute {
	return schema.StringAttribute{Description: desc, Optional: true}
}

func optionalDynamic(desc string) schema.Attribute {
	return schema.DynamicAttribute{Description: desc, Optional: true}
}

func extraAttribute() schema.Attribute {
	return optionalDynamic("Other midPoint items, merged at the object root, in midPoint naming (camelCase, as in " +
		"the REST API), e.g. yamldecode(file(\"extra.yaml\")). An item must not also be set by a typed attribute.")
}

var objectTemplateSpec = engineSpec{
	RestType: "objectTemplates", ShortName: "object_template",
	CreateOptions: []string{"isImport", "raw"}, UpdateOptions: []string{"raw"},
}

// objectTemplateState maps the midpoint_object_template schema.
type objectTemplateState struct {
	commonModel
	Name                   types.String  `tfsdk:"name"`
	Description            types.String  `tfsdk:"description"`
	LifecycleState         types.String  `tfsdk:"lifecycle_state"`
	Mapping                types.Dynamic `tfsdk:"mapping"`
	Item                   types.Dynamic `tfsdk:"item"`
	IterationSpecification types.Dynamic `tfsdk:"iteration_specification"`
	Extra                  types.Dynamic `tfsdk:"extra"`
}

func (s *objectTemplateState) fields() simpleFields {
	return simpleFields{
		Strings: []stringField{
			{"name", "name", &s.Name},
			{"description", "description", &s.Description},
			{"lifecycleState", "lifecycle_state", &s.LifecycleState},
		},
		Dynamics: []dynamicField{
			{"mapping", "mapping", &s.Mapping},
			{"item", "item", &s.Item},
			{"iterationSpecification", "iteration_specification", &s.IterationSpecification},
		},
		Extra: &s.Extra,
	}
}

// NewObjectTemplateResource returns the midpoint_object_template resource.
func NewObjectTemplateResource() resource.Resource {
	return simpleResource(objectTemplateSpec.ShortName, objectTemplateSpec, typedSchema(
		"A midPoint object template (ObjectTemplateType): mappings and item definitions applied to focus objects.",
		map[string]schema.Attribute{
			"name":            nameAttribute(),
			"description":     optionalString("Description."),
			"lifecycle_state": optionalString("Lifecycle state (lifecycleState), e.g. active, draft, proposed."),
			"mapping": optionalDynamic("Template mappings (mapping) in midPoint naming: a list of mappings, or one " +
				"mapping object. Always written, and read back after a change in midPoint, as a list."),
			"item": optionalDynamic("Item definitions (item) in midPoint naming: a list, or one object. Always " +
				"written, and read back after a change in midPoint, as a list."),
			"iteration_specification": optionalDynamic("Iteration specification (iterationSpecification) in " +
				"midPoint naming, e.g. { maxIterations = 5 }."),
			"extra": extraAttribute(),
		}, nil),
		(*objectTemplateState).fields)
}
