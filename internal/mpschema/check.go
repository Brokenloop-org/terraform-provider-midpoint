package mpschema

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
)

type unknownValue struct{}

// Unknown marks a value not known yet; the walker skips it.
var Unknown = unknownValue{}

// Finding is one problem found in a value.
type Finding struct {
	Path    string // "objectType[0].attibute"
	Key     string
	Type    string
	Problem string // sentence, without the path
}

// Check walks v as a value of rootType and reports what cannot be right.
// Wherever it cannot tell, it stops checking that subtree.
func (t *Table) Check(rootType string, v any) []Finding {
	var out []Finding
	t.walk(&out, "", Item{Type: rootType, Kind: KindComplex, Multi: true}, v)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (t *Table) walk(out *[]Finding, path string, it Item, v any) {
	switch x := v.(type) {
	case nil, unknownValue:
		return
	case []any:
		if len(x) > 1 && !it.Multi && it.Kind != KindOpen {
			*out = append(*out, Finding{Path: path, Key: lastKey(path), Type: it.Type,
				Problem: "takes a single value, but a list of several was given"})
		}
		for i, e := range x {
			t.walk(out, fmt.Sprintf("%s[%d]", path, i), it, e)
		}
	case map[string]any:
		t.walkObject(out, path, it, x)
	default:
		t.walkScalar(out, path, it, v)
	}
}

func (t *Table) walkObject(out *[]Finding, path string, it Item, m map[string]any) {
	if len(m) == 0 {
		return
	}
	// On a heterogeneous list, @element names what the value is, so the item's
	// type does not apply. Only an @type on an object says what it holds instead.
	if _, ok := m["@element"]; ok {
		_, typed := m["@type"]
		_, valued := m["@value"]
		if !typed || valued {
			return
		}
	}
	if val, ok := m["@value"]; ok {
		t.walk(out, path, it, val)
		return
	}
	switch it.Kind {
	case KindOpen:
		return
	case KindBoolean, KindInteger, KindEnum, KindSimple:
		if _, typed := m["@type"]; typed {
			return
		}
		*out = append(*out, Finding{Path: path, Key: lastKey(path), Type: it.Type,
			Problem: "takes a plain value, but an object was given"})
		return
	}
	typeName := it.Type
	if raw, ok := m["@type"]; ok {
		s, isStr := raw.(string)
		if !isStr {
			return
		}
		typeName = stripPrefix(s)
	}
	ty, ok := t.Types[typeName]
	if !ok || ty.Open {
		return
	}
	for _, key := range sortedKeys(m) {
		if strings.HasPrefix(key, "@") {
			continue
		}
		child := key
		if path != "" {
			child = path + "." + key
		}
		if strings.HasPrefix(key, "#") {
			*out = append(*out, Finding{Path: child, Key: key, Type: typeName,
				Problem: fmt.Sprintf("is not an item of %s: a key starting with # marks an item stored without a definition", typeName)})
			continue
		}
		sub, ok := ty.Items[stripPrefix(key)]
		if !ok {
			*out = append(*out, Finding{Path: child, Key: key, Type: typeName,
				Problem: fmt.Sprintf("is not an item of %s", typeName)})
			continue
		}
		t.walk(out, child, sub, m[key])
	}
}

func (t *Table) walkScalar(out *[]Finding, path string, it Item, v any) {
	bad := func(problem string) {
		*out = append(*out, Finding{Path: path, Key: lastKey(path), Type: it.Type, Problem: problem})
	}
	switch it.Kind {
	case KindEnum:
		s, ok := v.(string)
		if !ok {
			return
		}
		allowed, known := t.Enums[it.Type]
		if !known {
			return
		}
		for _, a := range allowed {
			if a == s {
				return
			}
		}
		bad(fmt.Sprintf("has the value %q, which is not one of %s (%s)", s, it.Type, strings.Join(allowed, ", ")))
	case KindBoolean:
		s, ok := v.(string)
		if !ok {
			return
		}
		switch strings.TrimSpace(s) {
		case "true", "false", "1", "0":
		default:
			bad(fmt.Sprintf("has the value %q, which is not a boolean", s))
		}
	case KindInteger:
		if !isInteger(v) {
			bad(fmt.Sprintf("has the value %v, which is not an integer", v))
		}
	}
}

// isInteger reports whether a scalar can be an integer. Kinds of scalar it
// does not recognise count as integers: it only says no when sure.
func isInteger(v any) bool {
	switch x := v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float64:
		return x == math.Trunc(x) && !math.IsInf(x, 0)
	case float32:
		return float64(x) == math.Trunc(float64(x))
	case json.Number:
		return bigIsInt(string(x))
	case string:
		return bigIsInt(strings.TrimSpace(x))
	}
	return true
}

func bigIsInt(s string) bool {
	f, _, err := big.ParseFloat(s, 10, 256, big.ToNearestEven)
	if err != nil {
		return false
	}
	return f.IsInt()
}

// stripPrefix drops a namespace prefix: the text after the last colon.
func stripPrefix(s string) string {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func lastKey(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.Index(path, "["); i >= 0 {
		path = path[:i]
	}
	return path
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
