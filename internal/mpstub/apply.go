package mpstub

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jplana/terraform-provider-midpoint/internal/delta"
)

// containerItems are the multi-valued container items whose values midPoint
// identifies by "@id". Single-valued containers (activation, ...) and
// structured properties (expression values, mapping sources, ...) carry no
// id.
var containerItems = map[string]bool{
	"assignment": true, "inducement": true, "authorization": true, "object": true,
	"objectType": true, "attribute": true, "association": true, "inbound": true,
	"outbound": true, "reaction": true, "mapping": true, "item": true,
	"objectClass": true, "assignmentRelation": true, "trigger": true, "panel": true,
}

// segment is one step of an item path: an item name, optionally with the id
// of one of its container values.
var segment = regexp.MustCompile(`^([^\[\]/]+)(?:\[(\d+)\])?$`)

// assignIDs gives every container value without an "@id" the next free id of
// the object, in a deterministic order (map keys sorted, list order).
func assignIDs(body map[string]any) {
	next := maxID(body)
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				if containerItems[k] {
					for _, e := range toList(t[k]) {
						if m, ok := e.(map[string]any); ok && m["@id"] == nil {
							next++
							m["@id"] = json.Number(strconv.Itoa(next))
						}
					}
				}
				walk(t[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(body)
}

func maxID(body map[string]any) int {
	highest := 0
	walkMaps(body, func(m map[string]any) {
		if n, err := strconv.Atoi(idString(m["@id"])); err == nil && n > highest {
			highest = n
		}
	})
	return highest
}

// idString renders a container id for comparison; "" when there is none.
func idString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case json.Number:
		return t.String()
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprint(t)
	}
}

// applyDelta applies one item delta to obj in place. path is a "/"-separated
// item path whose steps may address a container value by id ("item[id]"),
// at any depth.
func applyDelta(obj map[string]any, mod, path string, value any) error {
	steps := strings.Split(path, "/")
	parent := obj
	for _, step := range steps[:len(steps)-1] {
		name, id, err := parseSegment(step)
		if err != nil {
			return err
		}
		if id != "" {
			parent = findByID(parent[name], id)
			if parent == nil {
				return fmt.Errorf("no %s value with id %s", name, id)
			}
			continue
		}
		next, ok := parent[name].(map[string]any)
		if !ok {
			if parent[name] != nil {
				return fmt.Errorf("%s is not a single container value", name)
			}
			if mod == "delete" {
				return nil // deleting below a missing item changes nothing
			}
			next = map[string]any{}
			parent[name] = next
		}
		parent = next
	}

	name, id, err := parseSegment(steps[len(steps)-1])
	if err != nil {
		return err
	}
	if id != "" {
		return errors.New("path must end with an item name")
	}
	switch mod {
	case "replace":
		if len(toList(value)) == 0 {
			delete(parent, name)
		} else {
			parent[name] = value
		}
	case "add":
		parent[name] = addValues(name, parent[name], value)
	case "delete":
		if rest := deleteValues(name, parent[name], value); len(rest) == 0 {
			delete(parent, name)
		} else {
			parent[name] = rest
		}
	default:
		return fmt.Errorf("unknown modification type %q", mod)
	}
	return nil
}

func parseSegment(step string) (name, id string, err error) {
	m := segment.FindStringSubmatch(step)
	if m == nil {
		return "", "", fmt.Errorf("invalid path segment %q", step)
	}
	return m[1], m[2], nil
}

func findByID(item any, id string) map[string]any {
	for _, v := range toList(item) {
		if m, ok := v.(map[string]any); ok && idString(m["@id"]) == id {
			return m
		}
	}
	return nil
}

// addValues adds the values of value to item name; a value already present
// (same delta.Key) is not added again.
func addValues(name string, existing, value any) any {
	if existing == nil {
		return value
	}
	out := slices.Clone(toList(existing))
	for _, v := range toList(value) {
		k := delta.Key(name, v)
		if !slices.ContainsFunc(out, func(e any) bool { return delta.Key(name, e) == k }) {
			out = append(out, v)
		}
	}
	return out
}

// deleteValues removes the values of value from item name: {"@id": n}
// removes the container value with that id, anything else the value with
// the same delta.Key. Values not present are ignored, as midPoint does.
func deleteValues(name string, existing, value any) []any {
	out := slices.Clone(toList(existing))
	for _, v := range toList(value) {
		var match func(e any) bool
		if m, ok := v.(map[string]any); ok && len(m) == 1 && m["@id"] != nil {
			id := idString(m["@id"])
			match = func(e any) bool {
				em, ok := e.(map[string]any)
				return ok && idString(em["@id"]) == id
			}
		} else {
			k := delta.Key(name, v)
			match = func(e any) bool { return delta.Key(name, e) == k }
		}
		if i := slices.IndexFunc(out, match); i >= 0 {
			out = slices.Delete(out, i, i+1)
		}
	}
	return out
}
