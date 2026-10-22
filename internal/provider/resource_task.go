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
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var taskSpec = engineSpec{RestType: "tasks", ShortName: "task", CreateOptions: []string{"isImport"}}

const (
	executionRunnable  = "runnable"
	executionSuspended = "suspended"
	// importedKey is the private state key that marks a task imported
	// rather than created: its initial_execution_state is not known.
	importedKey = "imported"
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
		if ref, ok := rest["ownerRef"].(map[string]any); ok {
			if t, ok := refFromJSON(ref); ok && !t.OID.IsNull() && t.Filter.IsNull() &&
				(t.Type.IsNull() || t.Type.ValueString() == "UserType") && t.Relation.ValueString() == defaultRelation {
				s.Owner = t.OID
				delete(rest, "ownerRef")
			}
		}
	})
}

// replaceUnlessImported requires replacement when initial_execution_state
// changes on a task this provider created. On an imported task the value
// is not known (the task was created elsewhere), so a change is a no-op
// update rather than deleting and re-creating the task.
func replaceUnlessImported(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() {
		return
	}
	imported, diags := req.Private.GetKey(ctx, importedKey)
	resp.Diagnostics.Append(diags...)
	resp.RequiresReplace = len(imported) == 0
}

// NewTaskResource returns the midpoint_task resource.
func NewTaskResource() resource.Resource {
	return &typedResource[taskState]{
		name: taskSpec.ShortName,
		spec: taskSpec,
		schema: typedSchema(
			"A midPoint task (TaskType). The provider sets the execution state only when it creates the task; "+
				"suspending or resuming it in midPoint is an operation, not a change of configuration, and is never drift.",
			map[string]schema.Attribute{
				"name":        nameAttribute(),
				"description": optionalString("Description."),
				"owner": optionalString("OID of the owning user (ownerRef). When unset midPoint makes the calling " +
					"user the owner, and the owner is not compared."),
				"initial_execution_state": schema.StringAttribute{
					Description: "Execution state the task is created in: suspended (the default) or runnable. It is " +
						"written only on create; changing it re-creates the task. On an imported task it is recorded as " +
						"suspended and a change has no effect.",
					Optional: true,
					Computed: true,
					Default:  stringdefault.StaticString(executionSuspended),
					Validators: []validator.String{
						stringvalidator.OneOf(executionRunnable, executionSuspended),
					},
					PlanModifiers: []planmodifier.String{
						stringplanmodifier.RequiresReplaceIf(replaceUnlessImported,
							"Changing it re-creates a task the provider created.",
							"Changing it re-creates a task the provider created."),
					},
				},
				"binding": optionalString("Binding (binding): loose or tight. When unset midPoint defaults it " +
					"(loose) and it is not compared."),
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
		fromBody: taskFromBody,
		validate: simpleValidator(taskSpec.ShortName, (*taskState).fields),
		onImport: func(ctx context.Context, resp *resource.ImportStateResponse) {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("initial_execution_state"),
				types.StringValue(executionSuspended))...)
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, importedKey, []byte("true"))...)
		},
	}
}
