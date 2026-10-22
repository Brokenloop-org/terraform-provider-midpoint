package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jplana/terraform-provider-midpoint/internal/delta"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// assignmentModel maps one assignment or inducement block.
//
// condition, policy_rule, extra and construction.body are JSON text
// (jsontypes.Normalized: compared semantically at plan time), not dynamic
// values: terraform-plugin-framework does not support dynamic attributes
// inside repeatable blocks (Terraform turns such a block list into a tuple).
type assignmentModel struct {
	Target       *targetRef           `tfsdk:"target"`
	Construction *constructionModel   `tfsdk:"construction"`
	Order        types.Int64          `tfsdk:"order"`
	Condition    jsontypes.Normalized `tfsdk:"condition"`
	PolicyRule   jsontypes.Normalized `tfsdk:"policy_rule"`
	Extra        jsontypes.Normalized `tfsdk:"extra"`
}

// constructionModel maps a construction block.
type constructionModel struct {
	Resource types.String         `tfsdk:"resource"`
	Kind     types.String         `tfsdk:"kind"`
	Intent   types.String         `tfsdk:"intent"`
	Body     jsontypes.Normalized `tfsdk:"body"`
}

// assignmentItems are the container items an assignment block claims; its
// extra must not hold them.
var assignmentItems = []string{"targetRef", "construction", "order", "condition", "policyRule"}

// assignmentBlock is the schema of the assignment and inducement blocks.
func assignmentBlock(description string) schema.ListNestedBlock {
	jsonText := func(what string) schema.StringAttribute {
		return schema.StringAttribute{
			Description: what + " as JSON text in midPoint naming (camelCase, as in the REST API), e.g. " +
				"jsonencode({...}) or jsonencode(yamldecode(file(\"x.yaml\"))).",
			Optional:   true,
			CustomType: jsontypes.NormalizedType{},
			Validators: []validator.String{jsonTextValidator{}},
		}
	}
	return schema.ListNestedBlock{
		Description: description + " Values are matched to midPoint by content (target, construction, order), not by position.",
		NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{
				"order": schema.Int64Attribute{
					Description: "Assignment order; 2 makes an inducement apply to the members of the members.",
					Optional:    true,
				},
				"condition":   jsonText("Condition (MappingType)"),
				"policy_rule": jsonText("Policy rule (policyRule)"),
				"extra":       jsonText("Other items of this assignment (e.g. description, activation, focusType), an object,"),
			},
			Blocks: map[string]schema.Block{
				"target": targetBlock(),
				"construction": schema.SingleNestedBlock{
					Description: "Resource object construction.",
					Attributes: map[string]schema.Attribute{
						"resource": schema.StringAttribute{Description: "Resource OID.", Optional: true},
						"kind":     schema.StringAttribute{Description: "Object kind, e.g. account or entitlement.", Optional: true},
						"intent":   schema.StringAttribute{Description: "Object intent, e.g. default.", Optional: true},
						"body":     jsonText("Other construction items (attribute, association, ...), an object,"),
					},
				},
			},
		},
	}
}

// assignmentToJSON is the midPoint container value of block a at p.
func assignmentToJSON(a assignmentModel, p path.Path) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	out, d := jsonObjectAttr(a.Extra.StringValue, p.AtName("extra"))
	diags.Append(d...)
	for _, k := range assignmentItems {
		if _, ok := out[k]; ok {
			diags.AddAttributeError(p.AtName("extra"), "Conflicting assignment item",
				fmt.Sprintf("extra holds %q, which the block configures; set it in one place", k))
		}
	}
	if a.Target != nil {
		out["targetRef"] = refToJSON(*a.Target)
	}
	if c := a.Construction; c != nil {
		cp := p.AtName("construction")
		cons, d := jsonObjectAttr(c.Body.StringValue, cp.AtName("body"))
		diags.Append(d...)
		for name, v := range map[string]types.String{"resourceRef": c.Resource, "kind": c.Kind, "intent": c.Intent} {
			if v.IsNull() {
				continue
			}
			if _, ok := cons[name]; ok {
				diags.AddAttributeError(cp.AtName("body"), "Conflicting construction item",
					fmt.Sprintf("body holds %q, which the construction block configures; set it in one place", name))
			}
			if name == "resourceRef" {
				cons[name] = map[string]any{"oid": v.ValueString(), "type": "ResourceType"}
			} else {
				cons[name] = v.ValueString()
			}
		}
		out["construction"] = cons
	}
	if !a.Order.IsNull() && !a.Order.IsUnknown() {
		out["order"] = json.Number(strconv.FormatInt(a.Order.ValueInt64(), 10))
	}
	for name, v := range map[string]types.String{"condition": a.Condition.StringValue, "policyRule": a.PolicyRule.StringValue} {
		if val, ok, d := jsonAttr(v, p.AtName(snake(name))); ok {
			out[name] = val
		} else {
			diags.Append(d...)
		}
	}
	return out, diags
}

func snake(item string) string {
	if item == "policyRule" {
		return "policy_rule"
	}
	return item
}

// assignmentFromJSON is the block for the canonical container value v of
// item ("assignment" or "inducement"), with no "@id"s. ok is false when a
// block cannot express v (a target reference beyond oid, text filter, type
// and relation, or a non-integer order); the caller keeps such values in
// the resource extra. JSON text of prior blocks with the same identity is
// kept when it is canonically equal, so a refresh does not reformat it.
func assignmentFromJSON(restType, item string, v map[string]any, prior map[string]assignmentModel) (a assignmentModel, ok bool) {
	a = assignmentModel{Order: types.Int64Null(), Condition: jsontypes.NewNormalizedNull(), PolicyRule: jsontypes.NewNormalizedNull(), Extra: jsontypes.NewNormalizedNull()}
	rest := make(map[string]any, len(v))
	for k, e := range v {
		rest[k] = e
	}
	if tr, present := rest["targetRef"]; present {
		m, isMap := tr.(map[string]any)
		if !isMap {
			return a, false
		}
		t, refOK := refFromJSON(m)
		if !refOK {
			return a, false
		}
		a.Target = &t
		delete(rest, "targetRef")
	}
	if o, present := rest["order"]; present {
		n, isNum := o.(json.Number)
		if !isNum {
			return a, false
		}
		i, err := n.Int64()
		if err != nil {
			return a, false
		}
		a.Order = types.Int64Value(i)
		delete(rest, "order")
	}
	p, hasPrior := prior[delta.Key(item, v)]
	same := func(field string, priorText jsontypes.Normalized, actual any) bool {
		if !hasPrior {
			return false
		}
		pv, ok, _ := jsonAttr(priorText.StringValue, path.Empty())
		return ok && sameValue(restType, item, field, pv, actual)
	}
	if c, present := rest["construction"]; present {
		m, isMap := c.(map[string]any)
		if !isMap {
			return a, false
		}
		a.Construction = constructionFromJSON(m)
		if body := constructionRest(m); len(body) > 0 {
			if hasPrior && p.Construction != nil && same("construction", p.Construction.Body, body) {
				a.Construction.Body = p.Construction.Body
			} else {
				a.Construction.Body = jsontypes.NewNormalizedValue(renderJSON(body))
			}
		}
		delete(rest, "construction")
	}
	for name, dst := range map[string]*jsontypes.Normalized{"condition": &a.Condition, "policyRule": &a.PolicyRule} {
		val, present := rest[name]
		if !present {
			continue
		}
		delete(rest, name)
		priorText := p.Condition
		if name == "policyRule" {
			priorText = p.PolicyRule
		}
		if same(name, priorText, val) {
			*dst = priorText
		} else {
			*dst = jsontypes.NewNormalizedValue(renderJSON(val))
		}
	}
	if len(rest) > 0 {
		if same("", p.Extra, rest) {
			a.Extra = p.Extra
		} else {
			a.Extra = jsontypes.NewNormalizedValue(renderJSON(rest))
		}
	}
	return a, true
}

// constructionFromJSON is the construction block for the canonical
// construction m; constructionRest holds what goes in its body.
func constructionFromJSON(m map[string]any) *constructionModel {
	c := &constructionModel{Resource: types.StringNull(), Kind: types.StringNull(), Intent: types.StringNull(), Body: jsontypes.NewNormalizedNull()}
	if oid, ok := plainResourceRef(m["resourceRef"]); ok {
		c.Resource = types.StringValue(oid)
	}
	if s, ok := m["kind"].(string); ok {
		c.Kind = types.StringValue(s)
	}
	if s, ok := m["intent"].(string); ok {
		c.Intent = types.StringValue(s)
	}
	return c
}

func constructionRest(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	if _, ok := plainResourceRef(m["resourceRef"]); ok {
		delete(out, "resourceRef")
	}
	for _, k := range []string{"kind", "intent"} {
		if _, ok := m[k].(string); ok {
			delete(out, k)
		}
	}
	return out
}

// plainResourceRef returns the oid of a resource reference that the
// construction resource attribute expresses: an oid, type ResourceType (or
// none) and the default relation. Any other reference stays in the body.
func plainResourceRef(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	oid, ok := m["oid"].(string)
	if !ok {
		return "", false
	}
	for k, e := range m {
		switch k {
		case "oid":
		case "type":
			if e != "ResourceType" {
				return "", false
			}
		case "relation":
			if e != defaultRelation {
				return "", false
			}
		default:
			return "", false
		}
	}
	return oid, true
}

// assignmentPriors indexes the blocks of a prior state by the identity
// (delta.Key) of their canonical container values.
func assignmentPriors(restType, item string, blocks []assignmentModel) map[string]assignmentModel {
	out := make(map[string]assignmentModel, len(blocks))
	for _, b := range blocks {
		v, diags := assignmentToJSON(b, path.Root(item))
		if diags.HasError() {
			continue
		}
		c := normalize.Canonical(restType, map[string]any{item: []any{pruneEmpty(v)}})
		list, _ := c[item].([]any)
		if len(list) != 1 {
			continue
		}
		k := delta.Key(item, list[0])
		if _, seen := out[k]; !seen {
			out[k] = b
		}
	}
	return out
}

// sameValue reports whether desired and actual, the values of field of one
// value of item (field "" means the whole container value), are equal once
// canonicalized and aligned the way the engine compares them.
func sameValue(restType, item, field string, desired, actual any) bool {
	wrap := func(v any) map[string]any {
		if field != "" {
			v = map[string]any{field: v}
		}
		return map[string]any{item: []any{v}}
	}
	d := normalize.Canonical(restType, pruneEmpty(wrap(desired)))
	a := normalize.Canonical(restType, wrap(actual))
	d, a = delta.Align(d, a)
	return normalize.Equal(d, a)
}

// jsonAttr parses a JSON text attribute; ok is false when it is null,
// unknown or invalid (the last with an error diagnostic).
func jsonAttr(s types.String, p path.Path) (v any, ok bool, diags diag.Diagnostics) {
	text, known := knownValue(s)
	if !known {
		return nil, false, nil
	}
	v, err := parseJSON(text)
	if err != nil {
		diags.AddAttributeError(p, "Invalid JSON", err.Error())
		return nil, false, diags
	}
	return v, true, nil
}

// jsonObjectAttr parses a JSON object attribute; null yields an empty map.
func jsonObjectAttr(s types.String, p path.Path) (map[string]any, diag.Diagnostics) {
	v, ok, diags := jsonAttr(s, p)
	if !ok || v == nil {
		return map[string]any{}, diags
	}
	m, isMap := v.(map[string]any)
	if !isMap {
		diags.AddAttributeError(p, "Invalid JSON", "must be a JSON object")
		return map[string]any{}, diags
	}
	return m, diags
}

// parseJSON decodes one JSON value with numbers as json.Number.
func parseJSON(text string) (any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("not valid JSON: unexpected data after the value")
	}
	return v, nil
}

// renderJSON renders v the way Terraform's jsonencode does: compact, keys
// sorted, <, > and & escaped.
func renderJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Canonical values are maps, lists, strings, bools, json.Number
		// and nil; they always marshal.
		panic(fmt.Sprintf("rendering JSON: %v", err))
	}
	return string(b)
}

// jsonTextValidator requires a JSON object.
type jsonTextValidator struct{}

func (jsonTextValidator) Description(context.Context) string { return "must be a JSON object" }

func (v jsonTextValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (jsonTextValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	_, diags := jsonObjectAttr(req.ConfigValue, req.Path)
	resp.Diagnostics.Append(diags...)
}

// validateAssignment reports the conflicts in block a at p that the schema
// cannot: an empty block and items set both in a typed attribute and in
// JSON text.
func validateAssignment(a assignmentModel, p path.Path) diag.Diagnostics {
	var diags diag.Diagnostics
	if a.Target == nil && a.Construction == nil && a.Condition.IsNull() && a.PolicyRule.IsNull() && a.Extra.IsNull() {
		diags.AddAttributeError(p, "Empty block", "the block needs a target, a construction, a policy_rule or extra")
	}
	if extra, d := jsonObjectAttr(a.Extra.StringValue, p.AtName("extra")); !d.HasError() {
		for _, k := range assignmentItems {
			if _, ok := extra[k]; ok {
				diags.AddAttributeError(p.AtName("extra"), "Conflicting assignment item",
					fmt.Sprintf("extra holds %q, which the block configures; set it in one place", k))
			}
		}
	}
	if c := a.Construction; c != nil {
		if body, d := jsonObjectAttr(c.Body.StringValue, p.AtName("construction").AtName("body")); !d.HasError() {
			for _, k := range []string{"kind", "intent"} {
				if _, ok := body[k]; ok {
					diags.AddAttributeError(p.AtName("construction").AtName("body"), "Conflicting construction item",
						fmt.Sprintf("body holds %q; set it in the construction block", k))
				}
			}
			if _, ok := body["resourceRef"]; ok && !c.Resource.IsNull() {
				diags.AddAttributeError(p.AtName("construction").AtName("body"), "Conflicting construction item",
					"body holds \"resourceRef\", which the resource attribute configures; set it in one place")
			}
		}
	}
	return diags
}

// isArchetypeAssignment reports whether block a targets an archetype.
func isArchetypeAssignment(a assignmentModel) bool {
	return a.Target != nil && !a.Target.Type.IsNull() && !a.Target.Type.IsUnknown() && isArchetypeType(a.Target.Type.ValueString())
}
