// Package delta compares a desired canonical object body with the actual
// canonical body read from midPoint and computes the narrow item deltas of
// an ObjectModificationType: an apply changes exactly what the plan showed,
// never resends a whole object and never replaces a whole container because
// one field inside it changed.
package delta

import (
	"encoding/json"
	"math/big"
	"slices"
	"strconv"
	"strings"
)

// fallbackKeyPrefix starts the Key of a value identified by its whole
// content.
const fallbackKeyPrefix = "value:"

// defaultRelation is the relation midPoint gives a reference that has none.
const defaultRelation = "org:default"

// Key returns the identity of one value of item itemName, used to pair the
// values of a multi-valued item between the desired and the actual body:
//
//   - references (items named "...Ref"): oid, or the filter when there is no
//     oid, plus type and relation (absent relation is "org:default");
//   - assignment and inducement: the Key of targetRef when present, else the
//     construction's (resourceRef oid or filter, kind, intent); plus order;
//   - objectType: kind and intent;
//   - attribute and association: ref;
//   - anything else: the whole value, ignoring "@id", empty values
//     (emptyValue) and the order of map keys and of the values of
//     multi-valued items.
//
// Numbers are keyed by value, so 2 and 2.0 have the same key.
func Key(itemName string, v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return fallbackKeyPrefix + fingerprint(v)
	}
	switch {
	case isReference(itemName):
		return "ref:" + refKey(m)
	case itemName == "assignment" || itemName == "inducement":
		return assignmentKey(m)
	case itemName == "objectType":
		return "objectType:" + tuple(m["kind"], m["intent"])
	case itemName == "attribute" || itemName == "association":
		if m["ref"] != nil {
			return "itemRef:" + fingerprint(m["ref"])
		}
	}
	return fallbackKeyPrefix + fingerprint(m)
}

// refKey identifies a reference by oid (or filter when there is no oid),
// type and relation.
func refKey(m map[string]any) string {
	relation := m["relation"]
	if relation == nil {
		relation = defaultRelation
	}
	return tuple(refTarget(m), m["type"], relation)
}

// refTarget is the part of a reference that names its target: the oid when
// present, else the filter. A reference with both is identified by its oid.
func refTarget(m map[string]any) string {
	if m["oid"] != nil {
		return tuple("oid", m["oid"])
	}
	if m["filter"] != nil {
		return tuple("filter", m["filter"])
	}
	return tuple(nil)
}

func assignmentKey(m map[string]any) string {
	order := m["order"]
	if target, ok := m["targetRef"].(map[string]any); ok {
		return "assignment:" + tuple("target", refKey(target), order)
	}
	if c, ok := m["construction"].(map[string]any); ok {
		resource := tuple(nil)
		if r, ok := c["resourceRef"].(map[string]any); ok {
			resource = refTarget(r)
		}
		return "assignment:" + tuple("construction", resource, c["kind"], c["intent"], order)
	}
	return fallbackKeyPrefix + fingerprint(m)
}

// isReference reports whether item name holds ObjectReferenceType values
// (the same naming rule package normalize applies).
func isReference(name string) bool {
	return strings.HasSuffix(name, "Ref")
}

// tuple renders an ordered sequence of values; unlike a list, the order of
// its parts is significant.
func tuple(parts ...any) string {
	fs := make([]string, len(parts))
	for i, p := range parts {
		fs[i] = fingerprint(p)
	}
	return "(" + strings.Join(fs, ",") + ")"
}

// fingerprint renders v as canonical JSON-like text: map keys sorted, "@id"
// and nil-valued keys left out (normalize.Equal ignores both), list values
// sorted (the values of a multi-valued item are a set) and numbers by exact
// value. Two values with the same fingerprint are equal under
// normalize.Equal up to the order of list values.
// emptyValue reports whether v carries nothing: nil, a map with only "@id"
// and empty values, or a list of empty values. midPoint returns an empty
// container as {"@id": n} and a read's prior state drops empty values, so
// an empty item and an absent one identify the same value.
func emptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]any:
		for k, e := range t {
			if k != "@id" && !emptyValue(e) {
				return false
			}
		}
		return true
	case []any:
		for _, e := range t {
			if !emptyValue(e) {
				return false
			}
		}
		return true
	}
	return false
}

func fingerprint(v any) string {
	var b strings.Builder
	writeFingerprint(&b, v)
	return b.String()
}

func writeFingerprint(b *strings.Builder, v any) {
	if r, ok := numberValue(v); ok {
		b.WriteString(r.RatString())
		return
	}
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case string:
		b.WriteString(strconv.Quote(t))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k, e := range t {
			if k != "@id" && !emptyValue(e) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(k))
			b.WriteByte(':')
			writeFingerprint(b, t[k])
		}
		b.WriteByte('}')
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = fingerprint(e)
		}
		slices.Sort(parts)
		b.WriteByte('[')
		b.WriteString(strings.Join(parts, ","))
		b.WriteByte(']')
	default:
		// Not produced by JSON decoding; render it as JSON so the key is
		// still deterministic.
		raw, err := json.Marshal(t)
		if err != nil {
			raw = []byte(strconv.Quote(err.Error()))
		}
		b.Write(raw)
	}
}

// numberValue returns the exact value of a JSON number, float or integer.
func numberValue(v any) (*big.Rat, bool) {
	r := new(big.Rat)
	switch n := v.(type) {
	case json.Number:
		return r.SetString(n.String())
	case float64:
		if r.SetFloat64(n) == nil {
			return nil, false
		}
		return r, true
	case float32:
		if r.SetFloat64(float64(n)) == nil {
			return nil, false
		}
		return r, true
	case int:
		return r.SetInt64(int64(n)), true
	case int32:
		return r.SetInt64(int64(n)), true
	case int64:
		return r.SetInt64(n), true
	case uint64:
		return r.SetUint64(n), true
	}
	return nil, false
}
