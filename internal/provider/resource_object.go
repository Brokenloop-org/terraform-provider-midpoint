package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

var (
	_ resource.Resource                   = (*objectResource)(nil)
	_ resource.ResourceWithConfigure      = (*objectResource)(nil)
	_ resource.ResourceWithImportState    = (*objectResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*objectResource)(nil)
	_ resource.ResourceWithValidateConfig = (*objectResource)(nil)
)

// restTypePattern matches a REST collection type such as "roles" or
// "functionLibraries".
var restTypePattern = regexp.MustCompile(`^[a-z][A-Za-z]*$`)

// objectResource is midpoint_object: any midPoint object, given as a
// free-form body.
type objectResource struct {
	data *providerData
}

// objectModel maps the midpoint_object schema.
type objectModel struct {
	ID            types.String  `tfsdk:"id"`
	Type          types.String  `tfsdk:"type"`
	OID           types.String  `tfsdk:"oid"`
	Body          types.Dynamic `tfsdk:"body"`
	Name          types.String  `tfsdk:"name"`
	AdoptExisting types.Bool    `tfsdk:"adopt_existing"`
	ForceDestroy  types.Bool    `tfsdk:"force_destroy"`
	Options       types.List    `tfsdk:"options"`
}

// NewObjectResource returns the midpoint_object resource.
func NewObjectResource() resource.Resource {
	return &objectResource{}
}

func (r *objectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_object"
}

func (r *objectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description:   "The object OID.",
			Computed:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"type": schema.StringAttribute{
			Description:   "REST collection type of the object, e.g. roles or functionLibraries. Changing it replaces the object.",
			Required:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators: []validator.String{
				stringvalidator.RegexMatches(restTypePattern, "must be a midPoint REST collection type such as roles or functionLibraries"),
			},
		},
		"oid": schema.StringAttribute{
			Description: "Object OID. When unset, an oid in body is used, else midPoint generates one. " +
				"Changing it replaces the object. Creating an object whose oid already exists fails unless adopt_existing is set.",
			Optional: true,
			Computed: true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
				stringplanmodifier.RequiresReplaceIfConfigured(),
			},
		},
		"body": schema.DynamicAttribute{
			Description: "The object content without its type wrapper, e.g. yamldecode(file(\"library.yaml\")). " +
				"It is authoritative: items midPoint holds that body leaves out are removed, except server-managed ones.",
			Required: true,
		},
		"name": schema.StringAttribute{
			Description: "The object name, read from body.",
			Computed:    true,
		},
	}
	for k, v := range commonAttributes(objectSpec("")) {
		attrs[k] = v
	}
	resp.Schema = schema.Schema{
		Description: "A midPoint object of any type, managed through its free-form body.",
		Attributes:  attrs,
	}
}

func (r *objectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if data := configuredData(req, resp); data != nil {
		r.data = data
	}
}

// objectSpec is the engine spec for a midpoint_object of REST type typ.
func objectSpec(typ string) engineSpec {
	spec := engineSpec{RestType: typ, ShortName: "object", CreateOptions: []string{"isImport"}}
	switch typ {
	case "roles", "services", "orgs", "archetypes":
		spec.MembersCheck = roleMembersCheck
	case "resources":
		spec.MembersCheck = shadowsCheck
		spec.ReadOptions = []string{"noFetch"}
	}
	return spec
}

func (r *objectResource) engine(m objectModel) *engine {
	return newEngine(r.data, objectSpec(m.Type.ValueString()))
}

func (r *objectResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m objectModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Body.IsNull() || m.Body.IsUnknown() || m.Body.IsUnderlyingValueUnknown() {
		return
	}
	attrs, ok := bodyAttributes(m.Body)
	if !ok {
		resp.Diagnostics.AddAttributeError(path.Root("body"), "Invalid body",
			fmt.Sprintf("body must be an object (the midPoint object without its type wrapper), got %s", m.Body.UnderlyingValue().Type(ctx)))
		return
	}
	if bodyOID, ok := knownString(attrs["oid"]); ok && !m.OID.IsNull() && !m.OID.IsUnknown() && bodyOID != m.OID.ValueString() {
		resp.Diagnostics.AddAttributeError(path.Root("body"), "Conflicting oid",
			fmt.Sprintf("body oid %q differs from the oid attribute %q; set the oid in one place", bodyOID, m.OID.ValueString()))
	}
}

// bodyAttributes returns the top-level items of body when it is an object
// or a map.
func bodyAttributes(body types.Dynamic) (map[string]attr.Value, bool) {
	switch v := body.UnderlyingValue().(type) {
	case basetypes.ObjectValue:
		return v.Attributes(), true
	case basetypes.MapValue:
		return v.Elements(), true
	}
	return nil, false
}

// knownString returns v's value when it is a known, non-null string.
func knownString(v attr.Value) (string, bool) {
	if d, ok := v.(basetypes.DynamicValue); ok {
		v = d.UnderlyingValue()
	}
	s, ok := v.(basetypes.StringValue)
	if !ok || s.IsNull() || s.IsUnknown() {
		return "", false
	}
	return s.ValueString(), true
}

// desiredBody returns m's body as the engine's desired body, with the oid
// attribute (when known) injected as "oid".
func desiredBody(ctx context.Context, m objectModel) (map[string]any, diag.Diagnostics) {
	v, diags := dynToGo(ctx, m.Body)
	if diags.HasError() {
		return nil, diags
	}
	body, ok := v.(map[string]any)
	if !ok {
		diags.AddAttributeError(path.Root("body"), "Invalid body",
			"body must be an object (the midPoint object without its type wrapper)")
		return nil, diags
	}
	if !m.OID.IsNull() && !m.OID.IsUnknown() && m.OID.ValueString() != "" {
		oid := m.OID.ValueString()
		if b, ok := body["oid"].(string); ok && b != oid {
			diags.AddAttributeError(path.Root("body"), "Conflicting oid",
				fmt.Sprintf("body oid %q differs from the oid attribute %q; set the oid in one place", b, oid))
			return nil, diags
		}
		body["oid"] = oid
	}
	return body, diags
}

func (r *objectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	creating := req.State.Raw.IsNull()

	bodyOID := ""
	if attrs, ok := bodyAttributes(plan.Body); ok {
		bodyOID, _ = knownString(attrs["oid"])
	}
	if creating && plan.OID.IsUnknown() && bodyOID != "" {
		plan.OID = types.StringValue(bodyOID)
	}
	if !creating && bodyOID != "" {
		var state objectModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if bodyOID != state.OID.ValueString() {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("body"))
		}
	}

	plan.Name = types.StringUnknown()
	if v, d := dynToGo(ctx, plan.Body); !d.HasError() {
		if body, ok := v.(map[string]any); ok {
			plan.Name = canonicalName(plan.Type.ValueString(), body)
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, plan)...)

	if creating && plan.AdoptExisting.ValueBool() && !plan.OID.IsUnknown() && plan.OID.ValueString() != "" &&
		!plan.Type.IsUnknown() && r.data != nil {
		spec := objectSpec(plan.Type.ValueString())
		eng := newEngine(r.data, spec)
		existing, err := eng.c.Get(ctx, spec.RestType, plan.OID.ValueString(), eng.getOptions())
		switch {
		case err == nil:
			// The changes the adoption's update will send, computed from
			// the plan exactly as Create computes its desired body.
			var desired map[string]any
			if req.Config.Raw.IsFullyKnown() {
				if body, d := desiredBody(ctx, plan); !d.HasError() {
					desired = body
				}
			}
			resp.Diagnostics.AddWarning("Existing object will be adopted",
				eng.adoptionWarning(ctx, "midpoint_object", plan.OID.ValueString(), desired, existing.Body))
		case !errors.Is(err, client.ErrNotFound):
			tflog.Debug(ctx, "cannot check for an object to adopt", map[string]any{"type": spec.RestType, "oid": plan.OID.ValueString(), "error": err.Error()})
		}
	}
}

func (r *objectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	desired, diags := desiredBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	options, diags := optionsFromList(ctx, plan.Options)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	eng := r.engine(plan)
	oid, adopted, diags := eng.create(ctx, objectState{
		OID:           plan.OID.ValueString(),
		AdoptExisting: plan.AdoptExisting.ValueBool(),
		ForceDestroy:  plan.ForceDestroy.ValueBool(),
		Options:       options,
		Body:          desired,
	})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if adopted {
		// A failed update leaves an adopted object out of state: recorded,
		// it would be tainted and the next apply would replace (delete) it.
		desired["oid"] = oid
		resp.Diagnostics.Append(eng.update(ctx, oid, desired, options)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	plan.OID = types.StringValue(oid)
	plan.ID = types.StringValue(oid)
	plan.Name = canonicalName(plan.Type.ValueString(), desired)
	// Record the object before reading it back, so that it is never
	// orphaned; readAfterWrite then reports read-back failures as warnings,
	// because an error now would taint the object and the next apply would
	// replace (delete) it.
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.Append(eng.readAfterWrite(ctx, oid, desired, true)...)
}

func (r *objectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.OID = state.OID
	plan.ID = state.OID
	desired, diags := desiredBody(ctx, plan)
	resp.Diagnostics.Append(diags...)
	options, diags := optionsFromList(ctx, plan.Options)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	eng := r.engine(plan)
	resp.Diagnostics.Append(eng.update(ctx, state.OID.ValueString(), desired, options)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Name = canonicalName(plan.Type.ValueString(), desired)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.Append(eng.readAfterWrite(ctx, state.OID.ValueString(), desired, false)...)
}

func (r *objectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state objectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// After an import the body is null: compare against nothing, so that
	// the whole server body lands in state.
	desired := map[string]any{}
	if !state.Body.IsNull() {
		var diags diag.Diagnostics
		desired, diags = desiredBody(ctx, state)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	eng := r.engine(state)
	found, diags := eng.readInto(ctx, state.OID.ValueString(), desired, false, func(actual map[string]any) {
		body, d := goToDyn(ctx, stateBody(actual))
		resp.Diagnostics.Append(d...)
		state.Body = body
		state.Name = canonicalName(eng.spec.RestType, actual)
	})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *objectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state objectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	eng := r.engine(state)
	if resp.Diagnostics.Append(eng.refuseAdopted(state.OID.ValueString(), state.AdoptExisting.ValueBool(), state.ForceDestroy.ValueBool())...); resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(eng.delete(ctx, state.OID.ValueString(), state.ForceDestroy.ValueBool())...)
}

// ImportState takes "<type>/<oid>", e.g. "functionLibraries/<oid>"; Read
// then fills body from the server. A built-in (client.IsBuiltinOID) is
// recorded as adopted.
func (r *objectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	typ, oid, ok := strings.Cut(req.ID, "/")
	if !ok || !restTypePattern.MatchString(typ) || oid == "" || strings.Contains(oid, "/") {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("expected <type>/<oid>, e.g. functionLibraries/00000000-0000-0000-0000-000000000000, got %q", req.ID))
		return
	}
	for name, v := range map[string]attr.Value{
		"type":           types.StringValue(typ),
		"oid":            types.StringValue(oid),
		"id":             types.StringValue(oid),
		"adopt_existing": types.BoolValue(client.IsBuiltinOID(oid)),
		"force_destroy":  types.BoolValue(false),
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), v)...)
	}
}
