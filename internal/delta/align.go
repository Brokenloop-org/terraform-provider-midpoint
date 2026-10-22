package delta

import "strings"

// compareOnlyWhenConfigured are root items midPoint fills in when the
// configuration leaves them out. They are configuration (Canonical keeps
// them), but when the desired body does not set one, the server's value is
// not a difference.
var compareOnlyWhenConfigured = []string{
	"oid",      // midPoint generates the OID when the user leaves it unset
	"ownerRef", // TaskType.ownerRef: midPoint sets it to the calling user
	"binding",  // TaskType.binding: midPoint defaults it to "loose"
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
	"tasks": {"assignment"},
	// ResourceType.schema.definition: midPoint fetches the resource schema
	// from the connector (on a connection test, an import, any provisioning
	// operation) and caches it here, next to schema/cachingMetadata. A
	// configured (static) schema is compared; a cached one is not
	// configuration (recorded in normalize/testdata/real/resource-csv.json).
	"resources": {"schema/definition"},
}

// Align returns copies of desired and actual in which the actual body is
// normalized against the desired one, pairwise, so that normalize.Equal and
// Compute see only real differences:
//
//   - a reference whose desired value has a filter and no oid loses the
//     server-resolved oid; one whose desired value has an oid and no filter
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
// the root items of compareOnlyWhenConfiguredByType[restType] from the
// actual body when the desired body does not set them. Like Align it is
// idempotent and modifies neither input.
func AlignForType(restType string, desired, actual map[string]any) (d, a map[string]any) {
	d, a = Align(desired, actual)
	if a == nil {
		return d, a
	}
	for _, item := range compareOnlyWhenConfiguredByType[restType] {
		dropUnconfigured(desired, a, strings.Split(item, "/"))
	}
	return d, a
}

// dropUnconfigured deletes the item at path from a, an aligned copy that
// AlignForType owns, when desired does not set it. A container that the
// deletion leaves empty (nothing but "@id") is deleted too.
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
	dm, _ := desired[name].(map[string]any)
	dropUnconfigured(dm, am, path[1:])
	for k := range am {
		if k != "@id" {
			return
		}
	}
	delete(a, name)
}

// alignValue returns a copy of a, the actual value of item name, aligned to
// d, the desired value at the same position.
func alignValue(name string, d, a any) any {
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
