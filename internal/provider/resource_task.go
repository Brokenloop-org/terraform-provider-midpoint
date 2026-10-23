package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var taskSpec = engineSpec{RestType: "tasks", ShortName: "task", CreateOptions: []string{"isImport"}}

const (
	executionRunnable  = "runnable"
	executionSuspended = "suspended"
)

// initialStates maps initial_execution_state to the executionState and
// schedulingState a create writes.
var initialStates = map[string][2]string{
	executionRunnable:  {"runnable", "ready"},
	executionSuspended: {"suspended", "suspended"},
}

// taskState maps the midpoint_task schema.
type taskState struct {
	commonModel
	Name                  types.String  `tfsdk:"name"`
	Description           types.String  `tfsdk:"description"`
	Owner                 types.String  `tfsdk:"owner"`
	InitialExecutionState types.String  `tfsdk:"initial_execution_state"`
	Binding               types.String  `tfsdk:"binding"`
	Recurrence            types.String  `tfsdk:"recurrence"`
	Schedule              types.Dynamic `tfsdk:"schedule"`
	Activity              types.Dynamic `tfsdk:"activity"`
	Extra                 types.Dynamic `tfsdk:"extra"`
}

func (s *taskState) fields() simpleFields {
	return simpleFields{
		Strings: []stringField{
			{"name", "name", &s.Name},
			{"description", "description", &s.Description},
			{"binding", "binding", &s.Binding},
			{"recurrence", "recurrence", &s.Recurrence},
		},
		Dynamics: []dynamicField{
			{"schedule", "schedule", &s.Schedule},
			{"activity", "activity", &s.Activity},
		},
		Extra: &s.Extra,
		Reserved: map[string]string{
			"ownerRef":        "owner",
			"executionState":  "initial_execution_state",
			"schedulingState": "initial_execution_state",
		},
	}
}

// ownerRefValue is the ownerRef a task with owner oid holds.
func ownerRefValue(oid string) map[string]any {
	return map[string]any{"oid": oid, "type": "UserType"}
}

// taskToBody is the body of s. owner is written as ownerRef only when set,
// so that midPoint makes the caller the owner. create adds executionState
// and schedulingState from initial_execution_state: they are written only
// by a create and are server-managed afterwards (never compared), so a
// task suspended or resumed in midPoint is no drift.
func taskToBody(ctx context.Context, s *taskState, create bool) (map[string]any, diag.Diagnostics) {
	body, diags := simpleToBody(ctx, s.fields())
	if oid, ok := knownValue(s.Owner); ok {
		body["ownerRef"] = ownerRefValue(oid)
	}
	if create {
		initial := s.InitialExecutionState.ValueString()
		if _, ok := initialStates[initial]; !ok {
			initial = executionSuspended
		}
		body["executionState"] = initialStates[initial][0]
		body["schedulingState"] = initialStates[initial][1]
	}
	return body, diags
}

// taskFromBody sets s from actual. owner is read from ownerRef only when
// the prior state has owner set (an unset owner compares nothing: midPoint
// sets the caller); an ownerRef owner cannot express stays in extra.
func taskFromBody(ctx context.Context, s *taskState, actual map[string]any) diag.Diagnostics {
	prior := *s
	desired, _ := taskToBody(ctx, &prior, false)
	return simpleFromBody(ctx, taskSpec.RestType, desired, actual, prior.fields(), s.fields(), func(rest map[string]any) {
		s.Owner = types.StringNull()
		if prior.Owner.IsNull() {
			return
		}
		if oid, ok := ownerOID(rest["ownerRef"]); ok {
			s.Owner = oid
			delete(rest, "ownerRef")
		}
	})
}

// ownerOID is the owner attribute that expresses the ownerRef ref: a plain
// reference to a user by oid; ok is false for anything else.
func ownerOID(ref any) (types.String, bool) {
	m, ok := ref.(map[string]any)
	if !ok {
		return types.StringNull(), false
	}
	t, ok := refFromJSON(m)
	if !ok || t.OID.IsNull() || !t.Filter.IsNull() ||
		!(t.Type.IsNull() || t.Type.ValueString() == "UserType") || t.Relation.ValueString() != defaultRelation {
		return types.StringNull(), false
	}
	return t.OID, true
}

// Private state keys that mark an owner or binding an import recorded
// from the server rather than the configuration (see importedValue).
const (
	importedOwnerKey   = "imported_owner"
	importedBindingKey = "imported_binding"
)

// taskImportRead records the owner and binding of an imported task, which
// taskFromBody reads back only when the prior state sets them, and marks
// each one recorded: a configuration that sets them as midPoint holds them
// (the converter's) imports with no change, and one that leaves them unset
// keeps the recorded values (importedValue) without comparing them.
func taskImportRead(s *taskState, actual map[string]any) []string {
	var markers []string
	if s.Owner.IsNull() {
		if oid, ok := ownerOID(actual["ownerRef"]); ok {
			s.Owner = oid
			markers = append(markers, importedOwnerKey)
		}
	}
	if b, ok := actual["binding"].(string); ok && s.Binding.IsNull() {
		s.Binding = types.StringValue(b)
		markers = append(markers, importedBindingKey)
	}
	return markers
}

// importedValue plans owner and binding, which are optional and computed:
// the configured value when set (and the import marker key is cleared);
// unset, the state value when an import recorded it (key in private), so
// that it stays in state, tracking the server, without a change; else null.
// taskBodyModel leaves an unconfigured value out of the body, so it is
// never sent nor compared, as for an attribute that is unset.
type importedValue struct{ key string }

func (m importedValue) Description(context.Context) string {
	return "Unset, keeps the value an import recorded from midPoint, which is then neither sent nor compared; otherwise null."
}

func (m importedValue) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m importedValue) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	if !req.ConfigValue.IsNull() {
		if !req.State.Raw.IsNull() {
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, m.key, nil)...)
		}
		return
	}
	resp.PlanValue = types.StringNull()
	if req.State.Raw.IsNull() || req.StateValue.IsNull() {
		return
	}
	marker, diags := req.Private.GetKey(ctx, m.key)
	resp.Diagnostics.Append(diags...)
	if len(marker) > 0 {
		resp.PlanValue = req.StateValue
	}
}

// taskBodyModel is the model a create or update builds the body from: s
// without the owner and binding config does not set (an imported value
// importedValue kept in the plan).
func taskBodyModel(ctx context.Context, s *taskState, config tfsdk.Config) (*taskState, diag.Diagnostics) {
	var owner, binding types.String
	var diags diag.Diagnostics
	diags.Append(config.GetAttribute(ctx, path.Root("owner"), &owner)...)
	diags.Append(config.GetAttribute(ctx, path.Root("binding"), &binding)...)
	out := *s
	if owner.IsNull() {
		out.Owner = types.StringNull()
	}
	if binding.IsNull() {
		out.Binding = types.StringNull()
	}
	return &out, diags
}

// optionalImportedString is an optional, computed string attribute planned
// by importedValue with key.
func optionalImportedString(desc, key string) schema.Attribute {
	return schema.StringAttribute{
		Description:   desc,
		Optional:      true,
		Computed:      true,
		PlanModifiers: []planmodifier.String{importedValue{key: key}},
	}
}

// replaceUnlessPreexisting requires replacement when
// initial_execution_state changes on a task this provider created. On an
// imported or adopted task (preexistingKey) the value was never applied,
// so a change is a no-op update rather than deleting and re-creating a
// task the provider did not create.
func replaceUnlessPreexisting(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() {
		return
	}
	preexisting, diags := req.Private.GetKey(ctx, preexistingKey)
	resp.Diagnostics.Append(diags...)
	resp.RequiresReplace = len(preexisting) == 0
}

// NewTaskResource returns the midpoint_task resource.
func NewTaskResource() resource.Resource {
	return &typedResource[taskState]{
		name: taskSpec.ShortName,
		spec: taskSpec,
		schema: typedSchema(taskSpec,
			"A midPoint task (TaskType). The provider sets the execution state only when it creates the task; "+
				"suspending or resuming it in midPoint is an operation, not a change of configuration, and is never drift. "+
				"Assignments (in extra) are compared only when the task configures some: midPoint assigns a task the "+
				"archetype of its activity when it runs, so an assignment added in midPoint to a task that configures "+
				"none is not drift.",
			map[string]schema.Attribute{
				"name":        nameAttribute(),
				"description": optionalString("Description."),
				"owner": optionalImportedString("OID of the owning user (ownerRef). When unset midPoint makes the "+
					"calling user the owner, and the owner is not compared. An import records the owner midPoint "+
					"holds: a config that sets it imports with no change, and while the config leaves it unset the "+
					"recorded value stays in state, following midPoint, and is neither sent nor compared.", importedOwnerKey),
				"initial_execution_state": schema.StringAttribute{
					Description: "Execution state the task is created in: suspended (the default) or runnable. It is " +
						"written only on create; later suspending or resuming the task in midPoint is not drift. Changing " +
						"it re-creates a task the provider created, and removing the line counts as a change (back to " +
						"suspended). An imported or adopted task is never replaced for it: an import records suspended, " +
						"an adoption the configured value, and a change only updates the state.",
					Optional: true,
					Computed: true,
					Default:  stringdefault.StaticString(executionSuspended),
					Validators: []validator.String{
						stringvalidator.OneOf(executionRunnable, executionSuspended),
					},
					PlanModifiers: []planmodifier.String{
						stringplanmodifier.RequiresReplaceIf(replaceUnlessPreexisting,
							"Changing it re-creates a task the provider created; never an imported or adopted one.",
							"Changing it re-creates a task the provider created; never an imported or adopted one."),
					},
				},
				"binding": optionalImportedString("Binding (binding): loose or tight. When unset midPoint defaults "+
					"it (loose) and it is not compared. An import records the binding midPoint holds: a config that "+
					"sets it imports with no change, and while the config leaves it unset the recorded value stays "+
					"in state, following midPoint, and is neither sent nor compared.", importedBindingKey),
				"recurrence": optionalString("Recurrence (recurrence): single or recurring."),
				"schedule": optionalDynamic("Schedule (schedule) in midPoint naming, e.g. " +
					"{ recurrence = \"recurring\", interval = 3600 }."),
				"activity": optionalDynamic("Activity definition (activity) in midPoint naming, e.g. " +
					"yamldecode(file(\"recompute.yaml\"))."),
				"extra": extraAttribute(),
			}, nil),
		toBody: func(ctx context.Context, s *taskState, create, _ bool) (map[string]any, diag.Diagnostics) {
			return taskToBody(ctx, s, create)
		},
		fromBody:   taskFromBody,
		importRead: taskImportRead,
		bodyModel:  taskBodyModel,
		validate:   simpleValidator(taskSpec.ShortName, (*taskState).fields),
		onImport: func(ctx context.Context, resp *resource.ImportStateResponse) {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("initial_execution_state"),
				types.StringValue(executionSuspended))...)
		},
	}
}
