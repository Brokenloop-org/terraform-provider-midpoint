package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

// focusState is implemented by the pointer to each resource's state struct,
// which embeds focusModel and maps the kind-specific attributes.
type focusState interface {
	model() focusModel
	setModel(focusModel)
}

// focusResource is the shared implementation of the role-like resources.
// S is the resource's state struct and P its pointer type.
type focusResource[S any, P interface {
	*S
	focusState
}] struct {
	kind focusKind
	// attributes are the kind-specific schema attributes.
	attributes map[string]schema.Attribute
	data       *providerData
}

func (r *focusResource[S, P]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind.Name
}

func (r *focusResource[S, P]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Description: desc, Optional: true}
	}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description:   "The object OID.",
			Computed:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"oid": schema.StringAttribute{
			Description: "Object OID. When unset midPoint generates one. Changing it replaces the object.",
			Optional:    true,
			Computed:    true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
				stringplanmodifier.RequiresReplaceIfConfigured(),
			},
		},
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
			Description: "Schema extension items, e.g. { costCenter = \"CC-123\" }. Values are scalars or lists.",
			Optional:    true,
		},
		"extra": schema.DynamicAttribute{
			Description: "Other midPoint items, merged at the object root, in midPoint naming (camelCase, as in the " +
				"REST API), e.g. yamldecode(file(\"extra.yaml\")). An item must not also be set by a typed attribute; " +
				"assignment and inducement values here are added to the blocks (they hold the values a block cannot express).",
			Optional: true,
		},
	}
	for k, v := range commonAttributes() {
		attrs[k] = v
	}
	for k, v := range r.attributes {
		attrs[k] = v
	}
	resp.Schema = schema.Schema{
		Description: r.kind.Description,
		Attributes:  attrs,
		Blocks: map[string]schema.Block{
			"assignment": assignmentBlock("An assignment (membership, archetype, org)."),
			"inducement": assignmentBlock("An inducement: what holders of this object get."),
			"activation": schema.SingleNestedBlock{
				Description: "Activation. Other activation items (validity) go in extra.",
				Attributes: map[string]schema.Attribute{
					"administrative_status": str("Administrative status (administrativeStatus): enabled, disabled or archived."),
				},
			},
		},
	}
}

func (r *focusResource[S, P]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if data := configuredData(req, resp); data != nil {
		r.data = data
	}
}

func (r *focusResource[S, P]) engine() *engine {
	return newEngine(r.data, r.kind.Spec)
}

func (r *focusResource[S, P]) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	// A dynamic block whose for_each is not known yet leaves the whole list
	// unknown; it is validated once known.
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
	resp.Diagnostics.Append(validateFocus(ctx, P(&s).model(), r.kind)...)
}

func (r *focusResource[S, P]) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
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
	spec := r.kind.Spec
	_, err := r.data.Client.Get(ctx, spec.RestType, oid.ValueString(), client.GetOptions{Options: spec.ReadOptions, Exclude: readExcludes})
	switch {
	case err == nil:
		resp.Diagnostics.AddWarning("Existing object will be adopted",
			fmt.Sprintf("midpoint_%s %s/%s exists and will be adopted; the configured content is applied as an update",
				r.kind.Name, spec.RestType, oid.ValueString()))
	case !errors.Is(err, client.ErrNotFound):
		tflog.Debug(ctx, "cannot check for an object to adopt", map[string]any{"type": spec.RestType, "oid": oid.ValueString(), "error": err.Error()})
	}
}

func (r *focusResource[S, P]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var s S
	resp.Diagnostics.Append(req.Plan.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan := P(&s).model()
	options, diags := optionsFromList(ctx, plan.Options)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec := r.kind.Spec
	raw := slices.Contains(effectiveOptions(r.data.DefaultOptions, spec.ShortName, options, spec.CreateOptions), "raw")
	created, diags := focusToBody(ctx, plan, r.kind, raw)
	resp.Diagnostics.Append(diags...)
	// The update body never writes the computed archetype references.
	desired, diags := focusToBody(ctx, plan, r.kind, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	eng := r.engine()
	oid, adopted, diags := eng.create(ctx, objectState{
		OID:           plan.OID.ValueString(),
		AdoptExisting: plan.AdoptExisting.ValueBool(),
		ForceDestroy:  plan.ForceDestroy.ValueBool(),
		Options:       options,
		Body:          created,
	})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if adopted {
		// A failed update leaves an adopted object out of state (see
		// midpoint_object).
		resp.Diagnostics.Append(eng.update(ctx, oid, desired, options)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	plan.OID = types.StringValue(oid)
	plan.ID = types.StringValue(oid)
	P(&s).setModel(plan)
	// Recorded before the read-back, so the object is never orphaned.
	resp.Diagnostics.Append(resp.State.Set(ctx, &s)...)
	resp.Diagnostics.Append(eng.readAfterWrite(ctx, oid, desired, true)...)
}

func (r *focusResource[S, P]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var ps, ss S
	resp.Diagnostics.Append(req.Plan.Get(ctx, &ps)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &ss)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan, state := P(&ps).model(), P(&ss).model()
	plan.OID = state.OID
	plan.ID = state.OID
	desired, diags := focusToBody(ctx, plan, r.kind, false)
	resp.Diagnostics.Append(diags...)
	options, diags := optionsFromList(ctx, plan.Options)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	eng := r.engine()
	resp.Diagnostics.Append(eng.update(ctx, state.OID.ValueString(), desired, options)...)
	if resp.Diagnostics.HasError() {
		return
	}
	P(&ps).setModel(plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &ps)...)
	resp.Diagnostics.Append(eng.readAfterWrite(ctx, state.OID.ValueString(), desired, false)...)
}

func (r *focusResource[S, P]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var s S
	resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state := P(&s).model()
	// After an import only the oid is set: the body is empty and the whole
	// server object lands in state.
	desired, diags := focusToBody(ctx, state, r.kind, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	found, diags := r.engine().readInto(ctx, state.OID.ValueString(), desired, false, func(actual map[string]any) {
		m, d := focusFromBody(ctx, actual, state, r.kind)
		resp.Diagnostics.Append(d...)
		state = m
	})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	P(&s).setModel(state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &s)...)
}

func (r *focusResource[S, P]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var oid types.String
	var force types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("oid"), &oid)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("force_destroy"), &force)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.engine().delete(ctx, oid.ValueString(), force.ValueBool())...)
}

// ImportState takes the object OID; Read then fills everything from the
// server.
func (r *focusResource[S, P]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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
}
