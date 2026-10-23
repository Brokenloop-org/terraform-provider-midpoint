package delta

import (
	"strings"

	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// compareOnlyWhenConfigured are root items midPoint fills in when the
// configuration leaves them out. They are configuration (Canonical keeps
// them), but when the desired body does not set one, the server's value is
// not a difference.
var compareOnlyWhenConfigured = []string{
	"oid", // midPoint generates the OID when the user leaves it unset
}

// compareOnlyWhenConfiguredByType are items, per REST type, that midPoint
// fills in on objects of that type: compared only when the desired body
// sets them, like compareOnlyWhenConfigured. An item is a root item name or
// a path of single-valued containers ("schema/definition"). The whole item
// is compared as soon as the desired body has any value of it.
//
// Documented limitation: an assignment added to a task by hand is not
// drift unless the task configures assignments (a block or
// extra.assignment).
var compareOnlyWhenConfiguredByType = map[string][]string{
	// TaskType.assignment: when a task runs, midPoint assigns it the
	// archetype of its activity (e.g. Recomputation task, ...0502).
	// TaskType.ownerRef: midPoint sets it to the calling user.
	// TaskType.binding: midPoint defaults it to "loose".
	"tasks": {"assignment", "ownerRef", "binding"},
	// ResourceType.schema.definition: midPoint fetches the resource schema
	// from the connector (on a connection test, an import, any provisioning
	// operation) and caches it here, next to schema/cachingMetadata. A
	// configured (static) schema is compared; a cached one is not
	// configuration (recorded in normalize/testdata/real/resource-csv.json).
	"resources": {"schema/definition"},
}

// partialContainersByType are root single-valued containers, per REST type,
// that hold server-managed parts next to configuration: the parts
// Canonical strips or compareOnlyWhenConfiguredByType drops. The aligned
// bodies hold such a container on both sides whenever either side has it
// (an empty one on the other side), so that configuring or removing the
// configured part is a delta inside the container, never an add or replace
// of the whole container, which would replace or delete what midPoint
// manages there.
var partialContainersByType = map[string][]string{
	// ResourceType.capabilities: native (connector-reported) next to
	// configured; ResourceType.schema: the cached definition next to
	// generationConstraints.
	"resources": {"capabilities", "schema"},
}

// Align returns copies of desired and actual in which the actual body is
// normalized against the desired one, pairwise, so that normalize.Equal and
// Compute see only real differences:
//
//   - a reference whose desired value has a filter and no oid loses the
//     server-resolved oid; a structured actual filter whose text rendering
//     (normalize.FilterText) equals the desired text filter takes the
//     desired text form; one whose desired value has an oid and no filter
//     loses the filter; type and the "@type" annotation are dropped when the
//     desired value has none, and relation "org:default" when the desired
//     value has no relation;
//   - the values of a multi-valued item are paired by Key and the actual
//     list is reordered to the desired order (unpaired actual values last),
//     because the values of a multi-valued item are a set;
//   - root items in compareOnlyWhenConfigured are dropped from the actual
//     body when the desired body does not set them.
//
// Neither input is modified, and the aligned desired body equals desired.
func Align(desired, actual map[string]any) (d, a map[string]any) {
	d = deepCopyMap(desired)
	if actual == nil {
		return d, nil
	}
	a = alignMap(desired, actual)
	for _, name := range compareOnlyWhenConfigured {
		if desired[name] == nil {
			delete(a, name)
		}
	}
	return d, a
}

// AlignForType is Align for an object of REST type restType: it also drops
// the items of compareOnlyWhenConfiguredByType[restType] from the actual
// body when the desired body does not set them, and gives each side an
// empty container of partialContainersByType[restType] that only the other
// side has. Like Align it is idempotent and modifies neither input; the
// aligned desired body equals desired up to such empty containers (which
// never make two different bodies compare equal: the other side has
// content there).
func AlignForType(restType string, desired, actual map[string]any) (d, a map[string]any) {
	d, a = Align(desired, actual)
	if a == nil {
		return d, a
	}
	for _, item := range compareOnlyWhenConfiguredByType[restType] {
		dropUnconfigured(desired, a, strings.Split(item, "/"))
	}
	for _, name := range partialContainersByType[restType] {
		_, dm := d[name].(map[string]any)
		_, am := a[name].(map[string]any)
		switch {
		case dm && a[name] == nil:
			a[name] = map[string]any{}
		case am && d[name] == nil:
			d[name] = map[string]any{}
		}
	}
	return d, a
}

// dropUnconfigured deletes the item at path from a, an aligned copy that
// AlignForType owns, when desired does not set it. A container that the
// deletion leaves empty (nothing but "@id") is deleted too, unless desired
// has it.
func dropUnconfigured(desired, a map[string]any, path []string) {
	name := path[0]
	if len(path) == 1 {
		if desired[name] == nil {
			delete(a, name)
		}
		return
	}
	am, ok := a[name].(map[string]any)
	if !ok {
		return
	}
	dm, desiredHas := desired[name].(map[string]any)
	dropUnconfigured(dm, am, path[1:])
	if desiredHas {
		return
	}
	for k := range am {
		if k != "@id" {
			return
		}
	}
	delete(a, name)
}

// alignValue returns a copy of a, the actual value of item name, aligned to
// d, the desired value at the same position.
//
// midPoint returns a single value of a multi-valued item as that value, not
// as a one-element list. Canonical wraps the names in normalize.MultiValued;
// for any other item (an extension item, say) a one-element list of a scalar
// and that same scalar are the same item value, so the actual one takes the
// desired shape.
func alignValue(name string, d, a any) any {
	if v, ok := alignSingleValue(name, d, a); ok {
		return v
	}
	switch at := a.(type) {
	case map[string]any:
		dm, ok := d.(map[string]any)
		if !ok {
			return deepCopy(a)
		}
		if isReference(name) {
			return alignReference(dm, at)
		}
		return alignMap(dm, at)
	case []any:
		dl, ok := d.([]any)
		if !ok {
			return deepCopy(a)
		}
		return alignList(name, dl, at)
	default:
		return deepCopy(a)
	}
}

// alignSingleValue returns a in the shape of d when one of them is a
// one-element list holding a scalar and the other is that scalar, and name
// is not a normalize.MultiValued item (Canonical already makes those lists
// on both sides).
func alignSingleValue(name string, d, a any) (any, bool) {
	if normalize.MultiValued[name] {
		return nil, false
	}
	if dl, ok := d.([]any); ok && len(dl) == 1 && isScalar(dl[0]) && isScalar(a) && normalize.Equal(dl[0], a) {
		return []any{a}, true
	}
	if al, ok := a.([]any); ok && len(al) == 1 && isScalar(al[0]) && isScalar(d) && normalize.Equal(al[0], d) {
		return al[0], true
	}
	return nil, false
}

// isScalar reports whether v is a present value other than a map or a list.
func isScalar(v any) bool {
	switch v.(type) {
	case nil, map[string]any, []any:
		return false
	}
	return true
}

func alignMap(d, a map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		if dv, ok := d[k]; ok {
			out[k] = alignValue(k, dv, v)
		} else {
			out[k] = deepCopy(v)
		}
	}
	return out
}

func alignReference(d, a map[string]any) map[string]any {
	out := deepCopyMap(a)
	if textFilterMatches(d["filter"], a["filter"]) {
		out["filter"] = deepCopy(d["filter"])
	}
	if d["filter"] != nil && d["oid"] == nil {
		delete(out, "oid")
	}
	if d["oid"] != nil && d["filter"] == nil {
		delete(out, "filter")
	}
	if d["type"] == nil {
		delete(out, "type")
	}
	// Canonical keeps nested "@type"; the server may annotate a reference
	// with "ObjectReferenceType" that the configuration never states.
	if d["@type"] == nil {
		delete(out, "@type")
	}
	if d["relation"] == nil && out["relation"] == defaultRelation {
		delete(out, "relation")
	}
	return out
}

// textFilterMatches reports whether the desired filter d is a text filter
// ({"text": T}) and the actual filter a is a structured filter (not a text
// filter) whose text rendering (normalize.FilterText) is T. Such a filter is
// the one the configuration states, written in midPoint's structured form:
// the provider reads it as T, so it compares equal to {"text": T} and is
// never rewritten. Any other pair of filters compares as is.
func textFilterMatches(d, a any) bool {
	dm, ok := d.(map[string]any)
	if !ok || len(dm) != 1 {
		return false
	}
	want, ok := dm["text"].(string)
	if !ok {
		return false
	}
	am, ok := a.(map[string]any)
	if !ok {
		return false
	}
	if _, isText := am["text"]; isText {
		return false
	}
	got, ok := normalize.FilterText(am)
	return ok && got == want
}

func alignList(name string, d, a []any) []any {
	pairs, unpaired := pair(name, d, a)
	out := make([]any, 0, len(a))
	for i, j := range pairs {
		if j >= 0 {
			out = append(out, alignValue(name, d[i], a[j]))
		}
	}
	for _, j := range unpaired {
		out = append(out, deepCopy(a[j]))
	}
	return out
}

// Pair matches the values of the multi-valued item name between a desired
// and an actual list the way Align and Compute do: pairs[i] is the index in
// a of the value paired with d[i], or -1.
func Pair(name string, d, a []any) []int {
	pairs, _ := pair(name, d, a)
	return pairs
}

// pair matches the values of item name: pairs[i] is the index in a of the
// value paired with d[i], or -1; unpaired lists the indexes in a left over,
// in order. A desired value pairs with the first free actual value whose Key,
// after aligning it to the desired value, equals the desired value's Key.
func pair(name string, d, a []any) (pairs, unpaired []int) {
	used := make([]bool, len(a))
	pairs = make([]int, len(d))
	for i, dv := range d {
		pairs[i] = -1
		k := Key(name, dv)
		for j, av := range a {
			if !used[j] && keysMatch(name, k, dv, av) {
				pairs[i], used[j] = j, true
				break
			}
		}
	}
	for j := range a {
		if !used[j] {
			unpaired = append(unpaired, j)
		}
	}
	return pairs, unpaired
}

// keysMatch reports whether the actual value av has the desired key k. When
// the raw keys differ, av is aligned to dv first, since reference
// normalization can change a key; keys built only from kind/intent or from
// an item ref do not depend on references and are final.
func keysMatch(name, k string, dv, av any) bool {
	if Key(name, av) == k {
		return true
	}
	if !keyDependsOnReferences(name, dv, av) {
		return false
	}
	return Key(name, alignValue(name, dv, av)) == k
}

func keyDependsOnReferences(name string, dv, av any) bool {
	switch name {
	case "objectType":
		_, dm := dv.(map[string]any)
		_, am := av.(map[string]any)
		return !dm || !am
	case "attribute", "association":
		d, dm := dv.(map[string]any)
		a, am := av.(map[string]any)
		return !dm || !am || d["ref"] == nil || a["ref"] == nil
	}
	return true
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopy(e)
		}
		return out
	default:
		return v
	}
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopy(v)
	}
	return out
}
