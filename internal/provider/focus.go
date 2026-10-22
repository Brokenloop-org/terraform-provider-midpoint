package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/jplana/terraform-provider-midpoint/internal/delta"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// focusKind describes one role-like resource: midpoint_service,
// midpoint_role, midpoint_org or midpoint_archetype.
type focusKind struct {
	// Name is the resource type suffix, e.g. "service".
	Name string
	// Description is the resource description.
	Description string
	Spec        engineSpec
	// Authorization, Tenant and Archetype enable the kind-specific
	// attributes: authorization; tenant; holder_type and archetype_policy.
	Authorization, Tenant, Archetype bool
}

// focusModel maps the attributes of all four role-like resources. The
// kind-specific ones are tagged "-": each resource wraps the model in a
// struct that maps the ones its schema has (see focusState).
type focusModel struct {
	ID             types.String      `tfsdk:"id"`
	OID            types.String      `tfsdk:"oid"`
	Name           types.String      `tfsdk:"name"`
	DisplayName    types.String      `tfsdk:"display_name"`
	Description    types.String      `tfsdk:"description"`
	Identifier     types.String      `tfsdk:"identifier"`
	LifecycleState types.String      `tfsdk:"lifecycle_state"`
	RiskLevel      types.String      `tfsdk:"risk_level"`
	Requestable    types.Bool        `tfsdk:"requestable"`
	Delegable      types.Bool        `tfsdk:"delegable"`
	Archetype      types.String      `tfsdk:"archetype"`
	Assignments    []assignmentModel `tfsdk:"assignment"`
	Inducements    []assignmentModel `tfsdk:"inducement"`
	Activation     *activationModel  `tfsdk:"activation"`
	Extension      types.Dynamic     `tfsdk:"extension"`
	Extra          types.Dynamic     `tfsdk:"extra"`
	AdoptExisting  types.Bool        `tfsdk:"adopt_existing"`
	ForceDestroy   types.Bool        `tfsdk:"force_destroy"`
	Options        types.List        `tfsdk:"options"`

	Authorization   types.Dynamic `tfsdk:"-"` // all four kinds
	Tenant          types.Bool    `tfsdk:"-"` // org
	HolderType      types.List    `tfsdk:"-"` // archetype
	ArchetypePolicy types.Dynamic `tfsdk:"-"` // archetype
}

// activationModel maps the activation block.
type activationModel struct {
	AdministrativeStatus types.String `tfsdk:"administrative_status"`
}

// focusStrings and focusBools map typed scalar attributes to items.
func focusStrings(m *focusModel) map[string]*types.String {
	return map[string]*types.String{
		"name": &m.Name, "displayName": &m.DisplayName, "description": &m.Description,
		"identifier": &m.Identifier, "lifecycleState": &m.LifecycleState, "riskLevel": &m.RiskLevel,
	}
}

func focusBools(m *focusModel, kind focusKind) map[string]*types.Bool {
	out := map[string]*types.Bool{"requestable": &m.Requestable, "delegable": &m.Delegable}
	if kind.Tenant {
		out["tenant"] = &m.Tenant
	}
	return out
}

func focusDynamics(m *focusModel, kind focusKind) map[string]*types.Dynamic {
	out := map[string]*types.Dynamic{"extension": &m.Extension}
	if kind.Authorization {
		out["authorization"] = &m.Authorization
	}
	if kind.Archetype {
		out["archetypePolicy"] = &m.ArchetypePolicy
	}
	return out
}

// mergedItems may appear both as blocks and in extra: extra holds the
// values a block cannot express, and they are added to the block values.
var mergedItems = map[string]bool{"assignment": true, "inducement": true}

// archetypeRefValue is the reference to archetype oid.
func archetypeRefValue(oid string) map[string]any {
	return map[string]any{"oid": oid, "type": "ArchetypeType", "relation": defaultRelation}
}

const holderTypeIdentifier = "holderType"

// focusToBody is the midPoint object body for m. With raw (the create runs
// with the raw option) an archetype also writes archetypeRef and
// roleMembershipRef, which midPoint would otherwise compute, so that the GUI
// shows the archetype before a recompute; both are server-managed and never
// compared.
func focusToBody(ctx context.Context, m focusModel, kind focusKind, raw bool) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{}
	for item, v := range focusStrings(&m) {
		if s, ok := knownValue(*v); ok {
			body[item] = s
		}
	}
	for item, v := range focusBools(&m, kind) {
		if !v.IsNull() && !v.IsUnknown() {
			body[item] = v.ValueBool()
		}
	}
	if m.Activation != nil {
		if s, ok := knownValue(m.Activation.AdministrativeStatus); ok {
			body["activation"] = map[string]any{"administrativeStatus": s}
		}
	}

	var assignments []any
	if oid, ok := knownValue(m.Archetype); ok {
		assignments = append(assignments, map[string]any{"targetRef": archetypeRefValue(oid)})
		if raw {
			body["archetypeRef"] = archetypeRefValue(oid)
			body["roleMembershipRef"] = archetypeRefValue(oid)
		}
	}
	if kind.Archetype && !m.HolderType.IsNull() && !m.HolderType.IsUnknown() {
		var names []string
		diags.Append(m.HolderType.ElementsAs(ctx, &names, false)...)
		if len(names) > 0 {
			qnames := make([]any, len(names))
			for i, n := range names {
				qnames[i] = holderTypeQName(n)
			}
			assignments = append(assignments, map[string]any{
				"identifier":         holderTypeIdentifier,
				"assignmentRelation": []any{map[string]any{"holderType": qnames}},
			})
		}
	}
	for item, blocks := range map[string][]assignmentModel{"assignment": m.Assignments, "inducement": m.Inducements} {
		values := []any{}
		if item == "assignment" {
			values = assignments
		}
		for i, b := range blocks {
			v, d := assignmentToJSON(b, path.Root(item).AtListIndex(i))
			diags.Append(d...)
			values = append(values, v)
		}
		if len(values) > 0 {
			body[item] = values
		}
	}

	for item, v := range focusDynamics(&m, kind) {
		g, d := dynToGo(ctx, *v)
		diags.Append(d...)
		if g != nil {
			body[item] = g
		}
	}

	extra, d := dynToGo(ctx, m.Extra)
	diags.Append(d...)
	if extra != nil {
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
			case mergedItems[k]:
				body[k] = append(asList(body[k]), asList(v)...)
			case body[k] != nil:
				diags.AddAttributeError(path.Root("extra"), "Conflicting item",
					fmt.Sprintf("extra holds %q, which a typed attribute of this resource already sets; set it in one place", k))
			default:
				body[k] = v
			}
		}
	}
	return body, diags
}

// holderTypeQName is the midPoint QName of a holder type: "RoleType" is
// written "#RoleType", as midPoint returns it; a qualified name is kept.
func holderTypeQName(s string) string {
	if strings.ContainsAny(s, "#:{") {
		return s
	}
	return "#" + s
}

// qnameLocal is a type QName without its "#" or "c:" prefix.
func qnameLocal(s string) string {
	return strings.TrimPrefix(strings.TrimPrefix(s, "#"), "c:")
}

func asList(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	default:
		return []any{v}
	}
}

// focusFromBody is the model for actual, the canonical body midPoint
// returned (normalize.Canonical, "@id"s included), given the prior state.
//
// actual is first aligned with the body of prior the way the engine
// compares them (references, value order), so values that compare equal
// keep their configured shape. Each item goes to its typed attribute when
// the attribute can express it, else to extra; nothing is dropped.
// Assignments and inducements become blocks, except values a block cannot
// express, which go to extra. The archetype assignment folds into archetype
// only when prior has archetype set or prior is an import (no name yet):
// otherwise it is an assignment block, as configured. Likewise for the
// holderType assignment of an archetype and holder_type.
func focusFromBody(ctx context.Context, actual map[string]any, prior focusModel, kind focusKind) (focusModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	restType := kind.Spec.RestType
	desired, _ := focusToBody(ctx, prior, kind, false)
	_, aligned := delta.Align(normalize.Canonical(restType, pruneEmpty(desired)), actual)
	rest := stateBody(aligned)
	importing := prior.Name.IsNull()

	m := focusModel{
		ID: prior.ID, OID: prior.OID, AdoptExisting: prior.AdoptExisting, ForceDestroy: prior.ForceDestroy, Options: prior.Options,
		Archetype: types.StringNull(), Assignments: []assignmentModel{}, Inducements: []assignmentModel{},
		Extension: types.DynamicNull(), Extra: types.DynamicNull(),
		Authorization: types.DynamicNull(), Tenant: types.BoolNull(), HolderType: types.ListNull(types.StringType),
		ArchetypePolicy: types.DynamicNull(),
	}
	for item, dst := range focusStrings(&m) {
		*dst = types.StringNull()
		if s, ok := rest[item].(string); ok {
			*dst = types.StringValue(s)
			delete(rest, item)
		}
	}
	for item, dst := range focusBools(&m, kind) {
		*dst = types.BoolNull()
		if b, ok := rest[item].(bool); ok {
			*dst = types.BoolValue(b)
			delete(rest, item)
		}
	}
	if act, ok := rest["activation"].(map[string]any); ok && len(act) == 1 {
		if s, ok := act["administrativeStatus"].(string); ok {
			m.Activation = &activationModel{AdministrativeStatus: types.StringValue(s)}
			delete(rest, "activation")
		}
	}

	foldArchetype := importing || !prior.Archetype.IsNull()
	foldHolder := kind.Archetype && (importing || !prior.HolderType.IsNull())
	inPriorExtra := priorExtraValues(ctx, restType, prior.Extra)
	for _, item := range []string{"assignment", "inducement"} {
		values := asList(rest[item])
		delete(rest, item)
		priors := assignmentPriors(restType, item, prior.Assignments)
		if item == "inducement" {
			priors = assignmentPriors(restType, item, prior.Inducements)
		}
		var blocks []assignmentModel
		var unexpressed []any
		for _, v := range values {
			vm, isMap := v.(map[string]any)
			if !isMap {
				unexpressed = append(unexpressed, v)
				continue
			}
			// A value the prior state held in extra stays there, even when
			// a block could express it.
			if k := delta.Key(item, vm); inPriorExtra[item][k] > 0 {
				inPriorExtra[item][k]--
				unexpressed = append(unexpressed, v)
				continue
			}
			if item == "assignment" && foldArchetype && m.Archetype.IsNull() {
				if oid, ok := pureArchetypeAssignment(vm); ok {
					m.Archetype = types.StringValue(oid)
					continue
				}
			}
			if item == "assignment" && foldHolder && m.HolderType.IsNull() {
				if names, ok := holderTypeAssignment(vm); ok {
					m.HolderType = holderTypeList(names, prior.HolderType)
					continue
				}
			}
			if b, ok := assignmentFromJSON(restType, item, vm, priors); ok {
				blocks = append(blocks, b)
			} else {
				unexpressed = append(unexpressed, v)
			}
		}
		if item == "assignment" {
			m.Assignments = append(m.Assignments, blocks...)
		} else {
			m.Inducements = append(m.Inducements, blocks...)
		}
		if len(unexpressed) > 0 {
			rest[item] = unexpressed
		}
	}

	priorDyn := focusDynamics(&prior, kind)
	for item, dst := range focusDynamics(&m, kind) {
		v, present := rest[item]
		if !present {
			continue
		}
		delete(rest, item)
		var d diag.Diagnostics
		*dst, d = keepOrConvert(ctx, restType, item, *priorDyn[item], v)
		diags.Append(d...)
	}
	if len(rest) > 0 {
		var d diag.Diagnostics
		m.Extra, d = keepOrConvert(ctx, restType, "", prior.Extra, rest)
		diags.Append(d...)
	}
	return m, diags
}

// priorExtraValues counts, per item ("assignment", "inducement") and value
// identity (delta.Key), the values the prior extra holds.
func priorExtraValues(ctx context.Context, restType string, extra types.Dynamic) map[string]map[string]int {
	out := map[string]map[string]int{"assignment": {}, "inducement": {}}
	v, diags := dynToGo(ctx, extra)
	em, ok := v.(map[string]any)
	if diags.HasError() || !ok {
		return out
	}
	for item := range out {
		c := normalize.Canonical(restType, pruneEmpty(map[string]any{item: asList(em[item])}))
		for _, e := range asList(c[item]) {
			out[item][delta.Key(item, e)]++
		}
	}
	return out
}

// keepOrConvert is the dynamic attribute value for actual, the canonical
// value of root item (or of the extra items when item is ""): prior when
// it is canonically equal, so a refresh keeps the configured types.
func keepOrConvert(ctx context.Context, restType, item string, prior types.Dynamic, actual any) (types.Dynamic, diag.Diagnostics) {
	if !prior.IsNull() && !prior.IsUnknown() {
		if pv, d := dynToGo(ctx, prior); !d.HasError() {
			wrap := func(v any) map[string]any {
				if item == "" {
					m, _ := v.(map[string]any)
					return m
				}
				return map[string]any{item: v}
			}
			pd := normalize.Canonical(restType, pruneEmpty(wrap(pv)))
			pa := normalize.Canonical(restType, wrap(actual))
			if a, b := delta.Align(pd, pa); normalize.Equal(a, b) {
				return prior, nil
			}
		}
	}
	return goToDyn(ctx, actual)
}

// pureArchetypeAssignment returns the archetype oid of an assignment that
// holds nothing but a reference to an archetype by oid.
func pureArchetypeAssignment(v map[string]any) (string, bool) {
	if len(v) != 1 {
		return "", false
	}
	ref, ok := v["targetRef"].(map[string]any)
	if !ok {
		return "", false
	}
	t, ok := refFromJSON(ref)
	if !ok || t.OID.IsNull() || !t.Filter.IsNull() || t.Type.IsNull() || !isArchetypeType(t.Type.ValueString()) ||
		t.Relation.ValueString() != defaultRelation {
		return "", false
	}
	return t.OID.ValueString(), true
}

// holderTypeAssignment returns the holder types of the standard
// assignmentRelation assignment of an archetype.
func holderTypeAssignment(v map[string]any) ([]string, bool) {
	if len(v) != 2 || v["identifier"] != holderTypeIdentifier {
		return nil, false
	}
	rel, ok := v["assignmentRelation"].([]any)
	if !ok || len(rel) != 1 {
		return nil, false
	}
	r, ok := rel[0].(map[string]any)
	if !ok || len(r) != 1 {
		return nil, false
	}
	var names []string
	for _, h := range asList(r["holderType"]) {
		s, ok := h.(string)
		if !ok {
			return nil, false
		}
		names = append(names, qnameLocal(s))
	}
	return names, len(names) > 0
}

// holderTypeList is the holder_type value for names; prior when it writes
// the same QNames.
func holderTypeList(names []string, prior types.List) types.List {
	if !prior.IsNull() && !prior.IsUnknown() {
		var p []string
		if d := prior.ElementsAs(context.Background(), &p, false); !d.HasError() && len(p) == len(names) {
			same := true
			for i := range p {
				if qnameLocal(p[i]) != qnameLocal(names[i]) {
					same = false
				}
			}
			if same {
				return prior
			}
		}
	}
	elems := make([]attr.Value, len(names))
	for i, n := range names {
		elems[i] = types.StringValue(n)
	}
	return types.ListValueMust(types.StringType, elems)
}

// validateFocus reports configuration errors the schema cannot express: an
// archetype together with an explicit archetype assignment, extra items a
// typed attribute sets, and assignment block conflicts.
func validateFocus(ctx context.Context, m focusModel, kind focusKind) diag.Diagnostics {
	var diags diag.Diagnostics
	if !m.Archetype.IsNull() {
		for i, a := range m.Assignments {
			if isArchetypeAssignment(a) {
				diags.AddAttributeError(path.Root("assignment").AtListIndex(i).AtName("target").AtName("type"),
					"Conflicting archetype",
					"archetype is set and this assignment also targets an archetype; use either the archetype attribute or an archetype assignment block")
			}
		}
	}
	for item, blocks := range map[string][]assignmentModel{"assignment": m.Assignments, "inducement": m.Inducements} {
		for i, a := range blocks {
			diags.Append(validateAssignment(a, path.Root(item).AtListIndex(i))...)
		}
	}
	for item, v := range focusDynamics(&m, kind) {
		if item == "extension" && !v.IsNull() && !v.IsUnknown() && !v.IsUnderlyingValueUnknown() {
			if _, ok := bodyAttributes(*v); !ok {
				diags.AddAttributeError(path.Root("extension"), "Invalid extension", "extension must be an object of extension items")
			}
		}
	}
	if m.Activation != nil && m.Activation.AdministrativeStatus.IsNull() {
		diags.AddAttributeError(path.Root("activation"), "Incomplete activation", "activation requires administrative_status")
	}
	if m.Extra.IsNull() || m.Extra.IsUnknown() || m.Extra.IsUnderlyingValueUnknown() {
		return diags
	}
	attrs, ok := bodyAttributes(m.Extra)
	if !ok {
		diags.AddAttributeError(path.Root("extra"), "Invalid extra", "extra must be an object of midPoint items")
		return diags
	}
	for k, v := range attrs {
		if v.IsNull() {
			continue
		}
		if k == "oid" {
			diags.AddAttributeError(path.Root("extra"), "Conflicting oid", "extra must not hold oid; use the oid attribute")
			continue
		}
		if name, typed := typedItem(kind, k, v); typed {
			diags.AddAttributeError(path.Root("extra"), "Typed item in extra",
				fmt.Sprintf("%s is a typed attribute of midpoint_%s (%s); set it there instead of in extra", k, kind.Name, name))
		}
	}
	return diags
}

// typedItem reports whether the root item k with value v belongs to a
// typed attribute of kind (and names it): any value of a dynamic attribute
// item, and a value of a scalar or activation item that the attribute can
// express (a string, a bool, an activation with only administrativeStatus;
// unknown counts). A value the attribute cannot express (e.g. activation
// with validity) belongs in extra, where a read places it too.
func typedItem(kind focusKind, k string, v attr.Value) (string, bool) {
	if d, ok := v.(basetypes.DynamicValue); ok {
		if d.IsUnderlyingValueUnknown() {
			return focusAttrName(k), isTypedItem(kind, k)
		}
		v = d.UnderlyingValue()
	}
	unknown := v.IsUnknown()
	var m focusModel
	if _, ok := focusStrings(&m)[k]; ok {
		_, isStr := v.(basetypes.StringValue)
		return focusAttrName(k), unknown || isStr
	}
	if _, ok := focusBools(&m, kind)[k]; ok {
		_, isBool := v.(basetypes.BoolValue)
		return focusAttrName(k), unknown || isBool
	}
	if _, ok := focusDynamics(&m, kind)[k]; ok {
		return focusAttrName(k), true
	}
	if k == "activation" {
		act, ok := bodyAttributes(types.DynamicValue(v))
		if unknown || !ok {
			return "activation", unknown
		}
		_, isStr := act["administrativeStatus"].(basetypes.StringValue)
		return "activation", len(act) == 1 && isStr
	}
	return "", false
}

func isTypedItem(kind focusKind, k string) bool {
	var m focusModel
	_, s := focusStrings(&m)[k]
	_, b := focusBools(&m, kind)[k]
	_, d := focusDynamics(&m, kind)[k]
	return s || b || d || k == "activation"
}

// focusAttrName is the attribute name of root item k, e.g. display_name for
// displayName.
func focusAttrName(item string) string {
	var b strings.Builder
	for _, r := range item {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
