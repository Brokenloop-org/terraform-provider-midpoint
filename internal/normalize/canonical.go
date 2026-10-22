package normalize

import (
	"slices"
	"strings"
)

// keyPrefixes are the namespace prefixes the old JSON catalogue used on item
// names (common, types, query).
var keyPrefixes = []string{"c:", "t:", "q:"}

// polyStringKeys are the keys of a serialized PolyStringType.
var polyStringKeys = map[string]bool{"orig": true, "norm": true, "translation": true, "lang": true}

// defaultRelation is the relation midPoint stores on a reference that has
// none.
const defaultRelation = "org:default"

// Canonical returns the canonical form of body, an object of REST collection
// type restType without its type wrapper. It never mutates body.
//
// Canonical strips the c:, t: and q: prefixes from keys (and the c: prefix
// from reference type values and @type values), drops items matching
// ServerManaged (root @ns/@type, metadata, computed state), folds polystrings
// to their orig string, wraps single values of MultiValued items into
// one-element lists, gives references without a relation "org:default",
// prunes maps and lists that only became empty through stripping, and keeps
// container "@id"s.
func Canonical(restType string, body map[string]any) map[string]any {
	if body == nil {
		return nil
	}
	out, pruned := canonicalMap(restType, nil, body)
	if pruned {
		// The object itself is never pruned, even when nothing but
		// server-managed items was present.
		return map[string]any{}
	}
	return out
}

// canonicalMap returns a canonical copy of in, which sits at path, and
// whether it is to be pruned: it had content, and nothing but "@id" survived.
func canonicalMap(restType string, path []string, in map[string]any) (map[string]any, bool) {
	out := make(map[string]any, len(in))
	removed := false
	for name, key := range localNames(in) {
		v := in[key]
		if name == "@id" {
			out[name] = v
			continue
		}
		itemPath := append(slices.Clip(path), name)
		if IsServerManaged(restType, itemPath) {
			removed = true
			continue
		}
		cv, prune := canonicalValue(restType, itemPath, name, v)
		if prune {
			removed = true
			continue
		}
		if isReference(name) {
			cv = canonicalReference(cv)
		}
		if MultiValued[name] {
			if _, isList := cv.([]any); !isList {
				cv = []any{cv}
			}
		}
		out[name] = cv
	}
	if removed && onlyID(out) {
		return nil, true
	}
	return out, false
}

// canonicalValue canonicalizes the value v of item name at path; it reports
// whether the value is to be pruned.
func canonicalValue(restType string, path []string, name string, v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := polyString(t); ok {
			return s, false
		}
		return canonicalMap(restType, path, t)
	case []any:
		out := make([]any, 0, len(t))
		removed := false
		for _, e := range t {
			ce, prune := canonicalValue(restType, path, name, e)
			if prune {
				removed = true
				continue
			}
			out = append(out, ce)
		}
		if removed && len(out) == 0 {
			return nil, true
		}
		return out, false
	case string:
		if name == "@type" {
			return strings.TrimPrefix(t, "c:"), false
		}
		return t, false
	default:
		return v, false
	}
}

// localNames maps each local (prefix-stripped) key of m to the key it is read
// from. When a prefixed and an unprefixed spelling of the same item collide,
// the unprefixed one wins; between prefixed spellings the first in sort
// order wins, so the result is deterministic.
func localNames(m map[string]any) map[string]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	names := make(map[string]string, len(keys))
	for _, k := range keys {
		name := stripKeyPrefix(k)
		if prev, seen := names[name]; seen && prev == name {
			continue
		} else if seen && k != name {
			continue
		}
		names[name] = k
	}
	return names
}

func stripKeyPrefix(k string) string {
	for _, p := range keyPrefixes {
		if strings.HasPrefix(k, p) {
			return k[len(p):]
		}
	}
	return k
}

// polyString returns the orig string of a serialized PolyStringType: a map
// with a string "orig" and no keys other than orig, norm, translation, lang.
func polyString(m map[string]any) (string, bool) {
	var orig string
	found := false
	for k, v := range m {
		name := stripKeyPrefix(k)
		if !polyStringKeys[name] {
			return "", false
		}
		if name == "orig" {
			s, ok := v.(string)
			if !ok {
				return "", false
			}
			orig, found = s, true
		}
	}
	return orig, found
}

// isReference reports whether item name holds ObjectReferenceType values.
func isReference(name string) bool {
	return strings.HasSuffix(name, "Ref")
}

// canonicalReference strips the c: prefix from the type of each reference
// value in v and fills in the default relation. v is already a canonical
// copy, so it is modified in place.
func canonicalReference(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["type"].(string); ok {
			t["type"] = strings.TrimPrefix(s, "c:")
		}
		if t["relation"] == nil {
			t["relation"] = defaultRelation
		}
	case []any:
		for _, e := range t {
			canonicalReference(e)
		}
	}
	return v
}

func onlyID(m map[string]any) bool {
	for k := range m {
		if k != "@id" {
			return false
		}
	}
	return true
}
