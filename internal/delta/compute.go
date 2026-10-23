package delta

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/normalize"
)

// Modification types of an ItemDeltaType.
const (
	modAdd     = "add"
	modDelete  = "delete"
	modReplace = "replace"
)

// structuredProperties are items whose values are structured property values
// (beans such as ExpressionType), not containers: midPoint cannot address an
// item path inside them, so a change replaces the whole value. References
// ("...Ref" items) are treated the same way by name. Listing a container
// here only widens its delta to a whole-value replace, while missing a
// property would emit a path midPoint rejects, so the table errs toward
// listing.
var structuredProperties = map[string]bool{
	"expression": true, // ExpressionType: mapping and condition expressions
	"condition":  true, // MappingType.condition (ExpressionType); see containerUnder for AssignmentType.condition
	"filter":     true, // SearchFilterType: object selectors, references, queries
	"query":      true, // QueryType: activity object sets, collection queries
	"target":     true, // VariableBindingDefinitionType: mapping target
	"definition": true, // ResourceType.schema.definition (SchemaDefinitionType): an XML schema; midPoint cannot address the xsd:schema inside it and parses it only with its "@ns", which a whole-value replace carries
}

// containerUnder names items that structuredProperties lists but that are
// containers below the given parent items, where midPoint can address the
// items inside them.
var containerUnder = map[string][]string{
	"condition": {"assignment", "inducement"}, // AssignmentType.condition is a MappingType container
}

// protectedStringKeys mark a ProtectedStringType value (passwords, connector
// secrets): a property whose parts are never addressed separately.
var protectedStringKeys = []string{"clearValue", "encryptedData", "hashedData", "externalData"}

// Compute returns the item deltas that turn actual into desired. Both are
// canonical bodies (normalize.Canonical output); actual keeps the container
// "@id"s read from the server, which Compute uses to address container
// values. The bodies are aligned first (Align is idempotent, so aligned
// input is fine). The result lists deletes, then replaces, then adds, each
// in item-path order; it is empty when normalize.Equal holds on the aligned
// bodies. Compute never replaces the whole object: an error is returned
// when the difference cannot be expressed as item deltas.
func Compute(desired, actual map[string]any) ([]client.ItemDelta, error) {
	d, a := Align(desired, actual)
	if annotationsDiffer(d, a) {
		return nil, fmt.Errorf("delta: object annotations differ; a whole-object replace is never sent")
	}
	var c collector
	if err := c.compareMap("", "", d, a); err != nil {
		return nil, err
	}
	out := make([]client.ItemDelta, 0, len(c.deletes)+len(c.replaces)+len(c.adds))
	out = append(out, c.deletes...)
	out = append(out, c.replaces...)
	return append(out, c.adds...), nil
}

// collector gathers deltas by modification type, so that the result can
// list deletes, then replaces, then adds.
type collector struct {
	deletes, replaces, adds []client.ItemDelta
}

func (c *collector) emit(mod, path string, value any) {
	dl := client.ItemDelta{ModificationType: mod, Path: path, Value: value}
	switch mod {
	case modDelete:
		c.deletes = append(c.deletes, dl)
	case modReplace:
		c.replaces = append(c.replaces, dl)
	default:
		c.adds = append(c.adds, dl)
	}
}

// compareMap compares the items of two values of container item parent (both
// empty at the object root) at prefix. Annotations ("@" keys) are not items;
// callers compare them before descending.
func (c *collector) compareMap(prefix, parent string, d, a map[string]any) error {
	keys := make([]string, 0, len(d)+len(a))
	for k := range d {
		keys = append(keys, k)
	}
	for k := range a {
		if _, ok := d[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "@") {
			continue
		}
		dv, av := d[k], a[k]
		path := k
		if prefix != "" {
			path = prefix + "/" + k
		}
		switch {
		case isAbsent(dv) && isAbsent(av):
		case isAbsent(av):
			c.emit(modAdd, path, itemValue(k, dv))
		case isAbsent(dv):
			// midPoint treats a replace with no value as removing the item.
			c.emit(modReplace, path, nil)
		default:
			if err := c.compareItem(path, parent, k, dv, av); err != nil {
				return err
			}
		}
	}
	return nil
}

// compareItem compares the desired and actual values of item name (inside a
// value of container item parent) at path, both present.
func (c *collector) compareItem(path, parent, name string, dv, av any) error {
	if normalize.Equal(dv, av) {
		return nil
	}
	dl, dIsList := dv.([]any)
	al, aIsList := av.([]any)
	if dIsList && aIsList {
		if hasMap(dl) || hasMap(al) {
			return c.compareValues(path, parent, name, dl, al)
		}
		// Lists of scalars are replaced whole.
		c.emit(modReplace, path, dv)
		return nil
	}
	dm, dIsMap := dv.(map[string]any)
	am, aIsMap := av.(map[string]any)
	if dIsMap && aIsMap && descendable(parent, name, dm, am) {
		return c.compareMap(path, name, dm, am)
	}
	c.emit(modReplace, path, itemValue(name, dv))
	return nil
}

// compareValues compares the values of a multi-valued item holding
// container (or other structured) values. Values are paired by Key; unpaired
// desired values are added and unpaired actual values deleted, each in one
// delta; paired values that differ are compared item by item inside the
// container value when its "@id" is known, else deleted and re-added whole.
func (c *collector) compareValues(path, parent, name string, d, a []any) error {
	if len(d) == 1 && len(a) == 1 {
		id := idOf(a[0])
		// One value on each side and no id to address it by: this is often
		// a single-valued item that MultiValued wraps by name, so replace it.
		if id == nil {
			c.emit(modReplace, path, d)
			return nil
		}
		// One value on each side, addressable by id, and no identity other
		// than its whole content (outbound, inbound, mapping, ...): it is
		// the same value modified, so change it in place.
		dm, dIsMap := d[0].(map[string]any)
		am, aIsMap := a[0].(map[string]any)
		if dIsMap && aIsMap && strings.HasPrefix(Key(name, dm), fallbackKeyPrefix) && descendable(parent, name, dm, am) {
			n, err := containerID(id)
			if err != nil {
				return fmt.Errorf("delta: %s: %w", path, err)
			}
			return c.compareMap(path+"["+n+"]", name, dm, am)
		}
	}
	pairs, unpaired := pair(name, d, a)
	var deletes, adds []any
	for i, j := range pairs {
		if j < 0 {
			adds = append(adds, d[i])
			continue
		}
		if normalize.Equal(d[i], a[j]) {
			continue
		}
		dm, dIsMap := d[i].(map[string]any)
		am, aIsMap := a[j].(map[string]any)
		if id := idOf(a[j]); id != nil && dIsMap && aIsMap && descendable(parent, name, dm, am) {
			n, err := containerID(id)
			if err != nil {
				return fmt.Errorf("delta: %s: %w", path, err)
			}
			if err := c.compareMap(path+"["+n+"]", name, dm, am); err != nil {
				return err
			}
			continue
		}
		v, err := deleteValue(a[j])
		if err != nil {
			return fmt.Errorf("delta: %s: %w", path, err)
		}
		deletes = append(deletes, v)
		adds = append(adds, d[i])
	}
	for _, j := range unpaired {
		v, err := deleteValue(a[j])
		if err != nil {
			return fmt.Errorf("delta: %s: %w", path, err)
		}
		deletes = append(deletes, v)
	}
	if len(deletes) > 0 {
		c.emit(modDelete, path, deletes)
	}
	if len(adds) > 0 {
		c.emit(modAdd, path, adds)
	}
	return nil
}

// descendable reports whether a difference between two map values of item
// name, inside a value of container item parent, may be expressed by deltas
// on the items inside them: they must be container values (not references,
// structured properties or protected strings) and carry the same
// annotations (a different "@type" is a different value).
func descendable(parent, name string, d, a map[string]any) bool {
	if isReference(name) || isProtectedString(d) || isProtectedString(a) || annotationsDiffer(d, a) {
		return false
	}
	return !structuredProperties[name] || slices.Contains(containerUnder[name], parent)
}

func isProtectedString(m map[string]any) bool {
	for _, k := range protectedStringKeys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// annotationsDiffer reports whether the "@" keys other than "@id" differ.
func annotationsDiffer(d, a map[string]any) bool {
	for k, v := range d {
		if strings.HasPrefix(k, "@") && k != "@id" && !normalize.Equal(v, a[k]) {
			return true
		}
	}
	for k, v := range a {
		if _, ok := d[k]; !ok && strings.HasPrefix(k, "@") && k != "@id" && !normalize.Equal(nil, v) {
			return true
		}
	}
	return false
}

// itemValue is the value sent for item name: a list for a multi-valued item,
// the bare value otherwise.
func itemValue(name string, v any) any {
	if _, isList := v.([]any); !isList && normalize.MultiValued[name] {
		return []any{v}
	}
	return v
}

// isAbsent reports whether v stands for no value: nil or an empty list.
func isAbsent(v any) bool {
	if l, ok := v.([]any); ok {
		return len(l) == 0
	}
	return v == nil
}

func hasMap(l []any) bool {
	for _, v := range l {
		if _, ok := v.(map[string]any); ok {
			return true
		}
	}
	return false
}

func idOf(v any) any {
	if m, ok := v.(map[string]any); ok {
		return m["@id"]
	}
	return nil
}

// containerID renders a container "@id" as a plain integer.
func containerID(id any) (string, error) {
	if s, isString := id.(string); isString {
		id = json.Number(s)
	}
	r, ok := numberValue(id)
	if !ok || !r.IsInt() {
		return "", fmt.Errorf("container id %v is not an integer", id)
	}
	return r.Num().String(), nil
}

// deleteValue is the value sent to delete v: {"@id": n} for a container value
// with a known id (midPoint matches container values by id), else v whole.
func deleteValue(v any) (any, error) {
	id := idOf(v)
	if id == nil {
		return v, nil
	}
	n, err := containerID(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"@id": json.Number(n)}, nil
}

// NeedsFilterReevaluation reports whether an add or replace delta carries a
// reference (at any depth of its value) with a filter and no oid, which
// midPoint resolves only with the reevaluateSearchFilters option.
func NeedsFilterReevaluation(deltas []client.ItemDelta) bool {
	for _, dl := range deltas {
		if dl.ModificationType != modAdd && dl.ModificationType != modReplace {
			continue
		}
		if hasFilterOnlyReference(itemName(dl.Path), dl.Value) {
			return true
		}
	}
	return false
}

// itemName is the last item name of a delta path, without its container id.
func itemName(path string) string {
	name := path[strings.LastIndex(path, "/")+1:]
	if i := strings.Index(name, "["); i >= 0 {
		name = name[:i]
	}
	return name
}

func hasFilterOnlyReference(name string, v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if isReference(name) && t["filter"] != nil && t["oid"] == nil {
			return true
		}
		for k, e := range t {
			if hasFilterOnlyReference(k, e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasFilterOnlyReference(name, e) {
				return true
			}
		}
	}
	return false
}
