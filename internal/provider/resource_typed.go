package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

var (
	_ resource.Resource                   = (*typedResource[struct{}])(nil)
	_ resource.ResourceWithConfigure      = (*typedResource[struct{}])(nil)
	_ resource.ResourceWithImportState    = (*typedResource[struct{}])(nil)
	_ resource.ResourceWithModifyPlan     = (*typedResource[struct{}])(nil)
	_ resource.ResourceWithValidateConfig = (*typedResource[struct{}])(nil)
)

// typedResource is the create/read/update/delete lifecycle shared by the
// typed resources: the role-like ones, midpoint_object_template,
// midpoint_mark and midpoint_task. S is the resource's state struct; its
// schema has the attributes of identityAttributes and commonAttributes.
// What differs per resource is the schema and the conversion between S and
// the object body.
type typedResource[S any] struct {
	// name is the resource type suffix, e.g. "role".
	name   string
	spec   engineSpec
	schema schema.Schema
	// toBody returns the desired body of s, without the oid. create is set
	// for the body a create POSTs, and raw when that create runs with the
	// raw option. Updates, adoptions and comparisons use create = false.
	toBody func(ctx context.Context, s *S, create, raw bool) (map[string]any, diag.Diagnostics)
	// fromBody sets the kind's attributes of s, the prior state, from
	// actual, the canonical body midPoint returned ("@id"s included). It
	// keeps id, oid and the common attributes.
	fromBody func(ctx context.Context, s *S, actual map[string]any) diag.Diagnostics
	// validate checks the configuration; nil for none.
	validate func(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse)
	// onImport sets kind-specific attributes after an import by OID (which
	// also marks the object preexisting); nil for none.
	onImport func(ctx context.Context, resp *resource.ImportStateResponse)
	// observe sets the computed attributes of s from actual, the canonical
	// body of every read (refresh, import, the read-back after a write),
	// and with a nil actual to null before a create records the object, so
	// that a failed read-back leaves nothing unknown; nil for none.
	observe func(s *S, actual map[string]any)
	// bodyModel returns the model a create or an update builds the desired
	// body from: s, the plan, without the computed values that config, the
	// configuration, does not set (they are never sent); nil means s.
	bodyModel func(ctx context.Context, s *S, config tfsdk.Config) (*S, diag.Diagnostics)

	data *providerData
}

// configuredModel is the model a create or update builds the desired body
// from (bodyModel).
func (r *typedResource[S]) configuredModel(ctx context.Context, s *S, config tfsdk.Config) (*S, diag.Diagnostics) {
	if r.bodyModel == nil {
		return s, nil
	}
	return r.bodyModel(ctx, s, config)
}

// preexistingKey is the private state key that marks an object the
// provider did not create: it was imported or adopted. Create-only
// attributes (a task's initial_execution_state) are then not known, and a
// change to them never re-creates the object.
const preexistingKey = "preexisting"

// markPreexisting sets preexistingKey in private.
func markPreexisting(ctx context.Context, private interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}) diag.Diagnostics {
	return private.SetKey(ctx, preexistingKey, []byte("true"))
}

// commonModel maps identityAttributes and commonAttributes; the state
// structs of the simple typed resources embed it.
type commonModel struct {
	ID            types.String `tfsdk:"id"`
	OID           types.String `tfsdk:"oid"`
	AdoptExisting types.Bool   `tfsdk:"adopt_existing"`
	ForceDestroy  types.Bool   `tfsdk:"force_destroy"`
	Options       types.List   `tfsdk:"options"`
}

// identityAttributes are the id and oid attributes of the typed resources.
func identityAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description:   "The object OID.",
			Computed:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"oid": schema.StringAttribute{
			Description: "Object OID. When unset midPoint generates one. Changing it replaces the object. Creating an object " +
				"whose oid already exists fails unless adopt_existing is set.",
			Optional: true,
			Computed: true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
				stringplanmodifier.RequiresReplaceIfConfigured(),
			},
		},
	}
}

// typedSchema is a schema with attrs plus identityAttributes and
// commonAttributes.
func typedSchema(spec engineSpec, description string, attrs map[string]schema.Attribute, blocks map[string]schema.Block) schema.Schema {
	all := identityAttributes()
	for k, v := range commonAttributes(spec) {
		all[k] = v
	}
	for k, v := range attrs {
		all[k] = v
	}
	return schema.Schema{Description: description, Attributes: all, Blocks: blocks}
}

func (r *typedResource[S]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.name
}

func (r *typedResource[S]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.schema
}

func (r *typedResource[S]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if data := configuredData(req, resp); data != nil {
		r.data = data
	}
}

func (r *typedResource[S]) engine() *engine {
	return newEngine(r.data, r.spec)
}

// observingEngine is the engine whose reads run observe on s.
func (r *typedResource[S]) observingEngine(s *S) *engine {
	eng := r.engine()
	if r.observe != nil {
		eng.observe = func(actual map[string]any) { r.observe(s, actual) }
	}
	return eng
}

func (r *typedResource[S]) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if r.validate != nil {
		r.validate(ctx, req, resp)
	}
}

// attributeGetter is a plan or a state.
type attributeGetter interface {
	GetAttribute(ctx context.Context, p path.Path, target any) diag.Diagnostics
}

// commonValues reads oid, adopt_existing, force_destroy and options.
func commonValues(ctx context.Context, g attributeGetter) (st objectState, diags diag.Diagnostics) {
	var oid types.String
	var adopt, force types.Bool
	var options types.List
	diags.Append(g.GetAttribute(ctx, path.Root("oid"), &oid)...)
	diags.Append(g.GetAttribute(ctx, path.Root("adopt_existing"), &adopt)...)
	diags.Append(g.GetAttribute(ctx, path.Root("force_destroy"), &force)...)
	diags.Append(g.GetAttribute(ctx, path.Root("options"), &options)...)
	opts, d := optionsFromList(ctx, options)
	diags.Append(d...)
	return objectState{OID: oid.ValueString(), AdoptExisting: adopt.ValueBool(), ForceDestroy: force.ValueBool(), Options: opts}, diags
}

// setOID records oid as the oid and id attributes.
func setOID(ctx context.Context, state interface {
	SetAttribute(context.Context, path.Path, any) diag.Diagnostics
}, oid string) diag.Diagnostics {
	var diags diag.Diagnostics
	diags.Append(state.SetAttribute(ctx, path.Root("oid"), types.StringValue(oid))...)
	diags.Append(state.SetAttribute(ctx, path.Root("id"), types.StringValue(oid))...)
	return diags
}

// ModifyPlan warns, when creating with adopt_existing, that an existing
// object will be adopted, listing the changes the adoption will send.
func (r *typedResource[S]) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() || r.data == nil {
		return // destroy, update, or not configured
	}
	var oid types.String
	var adopt types.Bool
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("oid"), &oid)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("adopt_existing"), &adopt)...)
	if resp.Diagnostics.HasError() || !adopt.ValueBool() || oid.IsUnknown() || oid.ValueString() == "" {
		return
	}
	eng := r.engine()
	existing, err := eng.c.Get(ctx, r.spec.RestType, oid.ValueString(), eng.getOptions())
	switch {
	case err == nil:
		// The changes the adoption's update will send, computed from the
		// plan exactly as Create computes its desired body.
		var desired map[string]any
		if req.Config.Raw.IsFullyKnown() {
			var s S
			if d := req.Plan.Get(ctx, &s); !d.HasError() {
				if body, d := r.toBody(ctx, &s, false, false); !d.HasError() {
					desired = body
				}
			}
		}
		resp.Diagnostics.AddWarning("Existing object will be adopted",
			eng.adoptionWarning(ctx, "midpoint_"+r.name, oid.ValueString(), desired, existing.Body))
	case !errors.Is(err, client.ErrNotFound):
		tflog.Debug(ctx, "cannot check for an object to adopt", map[string]any{"type": r.spec.RestType, "oid": oid.ValueString(), "error": err.Error()})
	}
}

func (r *typedResource[S]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var s S
	resp.Diagnostics.Append(req.Plan.Get(ctx, &s)...)
	st, diags := commonValues(ctx, req.Plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	raw := slices.Contains(effectiveOptions(r.data.DefaultOptions, r.spec.ShortName, st.Options, r.spec.CreateOptions), "raw")
	bm, diags := r.configuredModel(ctx, &s, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, diags := r.toBody(ctx, bm, true, raw)
	resp.Diagnostics.Append(diags...)
	desired, diags := r.toBody(ctx, bm, false, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	eng := r.observingEngine(&s)
	st.Body = created
	oid, adopted, diags := eng.create(ctx, st)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if adopted {
		// A failed update leaves an adopted object out of state (see
		// midpoint_object).
		resp.Diagnostics.Append(eng.update(ctx, oid, desired, st.Options)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(markPreexisting(ctx, resp.Private)...)
	}
	// Recorded before the read-back, so the object is never orphaned.
	if r.observe != nil {
		r.observe(&s, nil)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &s)...)
	resp.Diagnostics.Append(setOID(ctx, &resp.State, oid)...)
	resp.Diagnostics.Append(eng.readAfterWrite(ctx, oid, desired, true)...)
	r.recordObserved(ctx, &s, &resp.State, oid, &resp.Diagnostics)
}

// recordObserved records s again after a read-back that ran observe on it.
func (r *typedResource[S]) recordObserved(ctx context.Context, s *S, state *tfsdk.State, oid string, diags *diag.Diagnostics) {
	if r.observe == nil {
		return
	}
	diags.Append(state.Set(ctx, s)...)
	diags.Append(setOID(ctx, state, oid)...)
}

func (r *typedResource[S]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var s S
	var oid types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &s)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("oid"), &oid)...)
	st, diags := commonValues(ctx, req.Plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	bm, diags := r.configuredModel(ctx, &s, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	desired, diags := r.toBody(ctx, bm, false, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	eng := r.observingEngine(&s)
	resp.Diagnostics.Append(eng.update(ctx, oid.ValueString(), desired, st.Options)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &s)...)
	resp.Diagnostics.Append(setOID(ctx, &resp.State, oid.ValueString())...)
	resp.Diagnostics.Append(eng.readAfterWrite(ctx, oid.ValueString(), desired, false)...)
	r.recordObserved(ctx, &s, &resp.State, oid.ValueString(), &resp.Diagnostics)
}

func (r *typedResource[S]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s S
	var oid types.String
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("oid"), &oid)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// After an import only the oid is set: the body is empty and the whole
	// server object lands in state.
	desired, diags := r.toBody(ctx, &s, false, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, diags := r.observingEngine(&s).readInto(ctx, oid.ValueString(), desired, false, func(actual map[string]any) {
		resp.Diagnostics.Append(r.fromBody(ctx, &s, actual)...)
	})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &s)...)
}

func (r *typedResource[S]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var oid types.String
	var adopt, force types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("oid"), &oid)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("adopt_existing"), &adopt)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("force_destroy"), &force)...)
	if resp.Diagnostics.HasError() {
		return
	}
	eng := r.engine()
	if resp.Diagnostics.Append(eng.refuseAdopted(oid.ValueString(), adopt.ValueBool(), force.ValueBool())...); resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(eng.delete(ctx, oid.ValueString(), force.ValueBool())...)
}

// ImportState takes the object OID; Read then fills everything from the
// server.
func (r *typedResource[S]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" || strings.ContainsAny(req.ID, "/ ") {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("expected the object OID, e.g. 00000000-0000-0000-0000-000000000000, got %q", req.ID))
		return
	}
	for name, v := range map[string]attr.Value{
		"oid":            types.StringValue(req.ID),
		"id":             types.StringValue(req.ID),
		"adopt_existing": types.BoolValue(false),
		"force_destroy":  types.BoolValue(false),
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), v)...)
	}
	resp.Diagnostics.Append(markPreexisting(ctx, resp.Private)...)
	if r.onImport != nil {
		r.onImport(ctx, resp)
	}
}
