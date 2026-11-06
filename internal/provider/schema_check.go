package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpschema"
)

// The values of on_invalid_item.
const (
	onInvalidItemError   = "error"
	onInvalidItemWarning = "warning"
)

// schemaCheck says where a dynamic attribute sits in the object: the items
// to follow from the resource's root type to the type the attribute holds.
type schemaCheck struct {
	Attr  path.Path // path.Root("schema_handling")
	Items []string  // {"schemaHandling"}; empty means the root type itself
}

// schemaChecks says what a resource's configuration is checked against.
type schemaChecks struct {
	// RootType is the midPoint type of the object, e.g. "RoleType"; empty
	// for no checks.
	RootType string
	Dynamics []schemaCheck
	// Blocks also checks the JSON text attributes of the assignment and
	// inducement blocks.
	Blocks bool
}

// assignmentBlockChecks are the JSON text attributes of an assignment block
// (Names: the attribute names from the block down) and the items to follow
// from AssignmentType to the type each holds.
var assignmentBlockChecks = []struct{ Names, Items []string }{
	{Names: []string{"extra"}},
	{Names: []string{"condition"}, Items: []string{"condition"}},
	{Names: []string{"policy_rule"}, Items: []string{"policyRule"}},
	{Names: []string{"construction", "body"}, Items: []string{"construction"}},
}

const assignmentType = "AssignmentType"

// focusSchemaChecks returns the checks of the role-like resource of kind.
func focusSchemaChecks(kind focusKind, rootType string) schemaChecks {
	c := schemaChecks{
		RootType: rootType,
		Dynamics: []schemaCheck{{Attr: path.Root("extra")}},
		Blocks:   true,
	}
	if kind.Authorization {
		c.Dynamics = append(c.Dynamics, schemaCheck{Attr: path.Root("authorization"), Items: []string{"authorization"}})
	}
	if kind.Archetype {
		c.Dynamics = append(c.Dynamics, schemaCheck{Attr: path.Root("archetype_policy"), Items: []string{"archetypePolicy"}})
	}
	return c
}

var (
	resourceSchemaChecks = schemaChecks{RootType: "ResourceType", Dynamics: []schemaCheck{
		{Attr: path.Root("schema_handling"), Items: []string{"schemaHandling"}},
		{Attr: path.Root("synchronization"), Items: []string{"synchronization"}},
		{Attr: path.Root("capabilities"), Items: []string{"capabilities", "configured"}},
		{Attr: path.Root("schema").AtName("generation_constraints"), Items: []string{"schema", "generationConstraints"}},
		{Attr: path.Root("extra")},
	}}
	taskSchemaChecks = schemaChecks{RootType: "TaskType", Dynamics: []schemaCheck{
		{Attr: path.Root("schedule"), Items: []string{"schedule"}},
		{Attr: path.Root("activity"), Items: []string{"activity"}},
		{Attr: path.Root("extra")},
	}}
	objectTemplateSchemaChecks = schemaChecks{RootType: "ObjectTemplateType", Dynamics: []schemaCheck{
		{Attr: path.Root("mapping"), Items: []string{"mapping"}},
		{Attr: path.Root("item"), Items: []string{"item"}},
		{Attr: path.Root("iteration_specification"), Items: []string{"iterationSpecification"}},
		{Attr: path.Root("extra")},
	}}
	markSchemaChecks = schemaChecks{RootType: "MarkType", Dynamics: []schemaCheck{
		{Attr: path.Root("display"), Items: []string{"display"}},
		{Attr: path.Root("event"), Items: []string{"eventMark"}},
		{Attr: path.Root("extra")},
	}}
)

// checkConfigItems checks the configuration of a resource against the
// provider's midPoint schema table and reports every invalid item on the
// attribute that holds it. It does nothing when the provider is not
// configured or has no table for the server's version.
func checkConfigItems(ctx context.Context, data *providerData, checks schemaChecks, config tfsdk.Config, resp *resource.ModifyPlanResponse) {
	if data == nil || data.SchemaTable == nil || checks.RootType == "" {
		return
	}
	rc := reporter{table: data.SchemaTable, warn: data.InvalidItemWarning, resp: resp}
	for _, c := range checks.Dynamics {
		var d types.Dynamic
		if config.GetAttribute(ctx, c.Attr, &d).HasError() {
			continue
		}
		rc.check(c.Attr, checks.RootType, c.Items, attrToCheckValue(d))
	}
	if checks.Blocks {
		for _, block := range []string{"assignment", "inducement"} {
			rc.checkBlocks(ctx, config, path.Root(block))
		}
	}
}

// checkObjectBody checks the body of a midpoint_object against the type its
// type attribute names. An unknown or unrecognised type is not checked.
func checkObjectBody(ctx context.Context, data *providerData, config tfsdk.Config, resp *resource.ModifyPlanResponse) {
	if data == nil || data.SchemaTable == nil {
		return
	}
	var typ types.String
	if config.GetAttribute(ctx, path.Root("type"), &typ).HasError() || typ.IsNull() || typ.IsUnknown() {
		return
	}
	rootType, ok := data.SchemaTable.Elements[client.SingularOf(typ.ValueString())]
	if !ok {
		return
	}
	var body types.Dynamic
	if config.GetAttribute(ctx, path.Root("body"), &body).HasError() {
		return
	}
	rc := reporter{table: data.SchemaTable, warn: data.InvalidItemWarning, resp: resp}
	rc.check(path.Root("body"), rootType, nil, attrToCheckValue(body))
}

// reporter turns findings into diagnostics on the plan response.
type reporter struct {
	table *mpschema.Table
	warn  bool
	resp  *resource.ModifyPlanResponse
}

// check walks v as the value reached by items from root and reports the
// findings on attrPath. When the items do not lead to a type, it stays silent.
func (rc reporter) check(attrPath path.Path, root string, items []string, v any) {
	typ, ok := rc.table.Descend(root, items...)
	if !ok {
		return
	}
	for _, f := range rc.table.Check(typ, v) {
		what := f.Problem
		if f.Path != "" {
			what = f.Path + " " + f.Problem
		}
		detail := fmt.Sprintf("%s: %s in midPoint %s. Fix it, or set on_invalid_item = \"warning\" if this midPoint version accepts it.",
			attrPath, what, rc.table.Line)
		if rc.warn {
			rc.resp.Diagnostics.AddAttributeWarning(attrPath, "Invalid midPoint item", detail)
		} else {
			rc.resp.Diagnostics.AddAttributeError(attrPath, "Invalid midPoint item", detail)
		}
	}
}

// checkBlocks checks the JSON text attributes of the assignment blocks at
// blocksPath. A list not known yet is skipped.
func (rc reporter) checkBlocks(ctx context.Context, config tfsdk.Config, blocksPath path.Path) {
	var list types.List
	if config.GetAttribute(ctx, blocksPath, &list).HasError() || list.IsNull() || list.IsUnknown() {
		return
	}
	for i, e := range list.Elements() {
		obj, ok := e.(basetypes.ObjectValue)
		if !ok || obj.IsNull() || obj.IsUnknown() {
			continue
		}
		blockPath := blocksPath.AtListIndex(i)
		for _, c := range assignmentBlockChecks {
			v, ok := nestedAttr(obj, c.Names)
			if !ok {
				continue
			}
			s, ok := knownJSONText(v)
			if !ok {
				continue
			}
			attrPath := blockPath
			for _, name := range c.Names {
				attrPath = attrPath.AtName(name)
			}
			var parsed any
			dec := json.NewDecoder(bytes.NewReader([]byte(s)))
			dec.UseNumber()
			if dec.Decode(&parsed) != nil {
				continue // malformed JSON is the attribute validator's to report
			}
			rc.check(attrPath, assignmentType, c.Items, parsed)
		}
	}
}

// nestedAttr returns the attribute of obj reached through names.
func nestedAttr(obj basetypes.ObjectValue, names []string) (attr.Value, bool) {
	var cur attr.Value = obj
	for _, name := range names {
		o, ok := cur.(basetypes.ObjectValue)
		if !ok || o.IsNull() || o.IsUnknown() {
			return nil, false
		}
		cur, ok = o.Attributes()[name]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// knownJSONText returns the text of a known, non-null, non-empty string value.
func knownJSONText(v attr.Value) (string, bool) {
	sv, ok := v.(basetypes.StringValuable)
	if !ok {
		return "", false
	}
	s, diags := sv.ToStringValue(context.Background())
	if diags.HasError() || s.IsNull() || s.IsUnknown() || s.ValueString() == "" {
		return "", false
	}
	return s.ValueString(), true
}

// attrToCheckValue converts a configuration value to the shape the walker
// reads: maps, lists, strings, booleans and json.Number. Null becomes nil
// and every unknown part becomes mpschema.Unknown.
func attrToCheckValue(v attr.Value) any {
	if v == nil || v.IsNull() {
		return nil
	}
	if v.IsUnknown() {
		return mpschema.Unknown
	}
	switch tv := v.(type) {
	case basetypes.DynamicValue:
		if tv.IsUnderlyingValueNull() {
			return nil
		}
		if tv.IsUnderlyingValueUnknown() {
			return mpschema.Unknown
		}
		return attrToCheckValue(tv.UnderlyingValue())
	case basetypes.StringValue:
		return tv.ValueString()
	case basetypes.BoolValue:
		return tv.ValueBool()
	case basetypes.NumberValue:
		return json.Number(tv.ValueBigFloat().Text('f', -1))
	case basetypes.ObjectValue:
		return attrMapToCheckValue(tv.Attributes())
	case basetypes.MapValue:
		return attrMapToCheckValue(tv.Elements())
	case basetypes.TupleValue:
		return attrSliceToCheckValue(tv.Elements())
	case basetypes.ListValue:
		return attrSliceToCheckValue(tv.Elements())
	case basetypes.SetValue:
		return attrSliceToCheckValue(tv.Elements())
	}
	return mpschema.Unknown // a kind of value it does not know: leave it alone
}

func attrMapToCheckValue(in map[string]attr.Value) map[string]any {
	out := make(map[string]any, len(in))
	for k, e := range in {
		out[k] = attrToCheckValue(e)
	}
	return out
}

func attrSliceToCheckValue(in []attr.Value) []any {
	out := make([]any, len(in))
	for i, e := range in {
		out[i] = attrToCheckValue(e)
	}
	return out
}
