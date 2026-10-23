package normalize

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// filterList is v as a list: nil for nil, a one-element list for a single value.
func filterList(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	default:
		return []any{v}
	}
}

// axiomString quotes s for a midPoint text query .
func axiomString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// FilterText is the text query (Axiom) of the canonical search filter m: a
// text filter as is, or a structured filter built of equal conditions on a
// path with one scalar value, joined by and, or and not. The operands of an
// and or an or are sorted (as rendered text), so two structured filters that
// list the same operands in a different order have the same text. ok is false
// for any other filter. The converter writes this text into the configuration and the
// provider reads a structured filter stored in midPoint as the same text, so
// both sides agree byte for byte.
func FilterText(m map[string]any) (string, bool) {
	if m == nil {
		return "", false
	}
	if t, ok := m["text"].(string); ok && len(m) == 1 {
		return t, true
	}
	// A node with one and/or child is that child, without its parentheses.
	for len(m) == 1 {
		var k string
		for k = range m {
		}
		child, ok := m[k].(map[string]any)
		if !ok || (k != "and" && k != "or") {
			break
		}
		if k == "or" {
			return filterJoin(child, " or ", true)
		}
		m = child
	}
	return filterJoin(m, " and ", true)
}

// filterJoin renders the children of a filter node joined by sep: the node's
// own children are conjuncts (and), the children of an or node alternatives.
// A compound with several parts below the top is parenthesized.
func filterJoin(node map[string]any, sep string, top bool) (string, bool) {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(node)) {
		for _, child := range filterList(node[k]) {
			cm, ok := child.(map[string]any)
			if !ok {
				return "", false
			}
			var s string
			switch k {
			case "equal":
				s, ok = equalText(cm)
			case "and":
				s, ok = filterJoin(cm, " and ", false)
			case "or":
				s, ok = filterJoin(cm, " or ", false)
			case "not":
				s, ok = filterJoin(cm, " and ", true)
				s = "not (" + s + ")"
			default:
				ok = false
			}
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	// The operands of and/or are a set: sorted, the text does not depend on
	// the order midPoint (or a hand-written filter) lists them in.
	slices.Sort(parts)
	s := strings.Join(parts, sep)
	if !top && len(parts) > 1 {
		s = "(" + s + ")"
	}
	return s, true
}

// equalText renders an equal condition: path = value, for a path and one
// scalar value and nothing else (no matching rule).
func equalText(c map[string]any) (string, bool) {
	path, ok := c["path"].(string)
	if !ok || len(c) != 2 {
		return "", false
	}
	vals := filterList(c["value"])
	if len(vals) != 1 {
		return "", false
	}
	switch v := vals[0].(type) {
	case string:
		return path + " = " + axiomString(v), true
	case bool:
		return path + " = " + strconv.FormatBool(v), true
	case float64:
		return path + " = " + strconv.FormatFloat(v, 'f', -1, 64), true
	case json.Number:
		return path + " = " + v.String(), true
	}
	return "", false
}
