package convert

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

const defaultRelation = "org:default"

// focus projects a role, service, org or archetype. It inverts the
// provider's focusToBody (internal/provider/focus.go).
func (p *projector) focus(restType string) {
	m := simpleMap{
		strings: map[string]string{
			"name": "name", "displayName": "display_name", "description": "description",
			"identifier": "identifier", "lifecycleState": "lifecycle_state", "riskLevel": "risk_level",
		},
		dynamics: map[string]string{"extension": "extension", "authorization": "authorization"},
	}
	if restType == "archetypes" {
		m.dynamics["archetypePolicy"] = "archetype_policy"
	}
	p.takeAll(m)
	p.takeBool("requestable", "requestable")
	p.takeBool("delegable", "delegable")
	if restType == "orgs" {
		p.takeBool("tenant", "tenant")
	}

	// activation { administrative_status } only when that is all it holds.
	if act, ok := p.rest["activation"].(map[string]any); ok && len(act) == 1 {
		if s, ok := act["administrativeStatus"].(string); ok {
			p.r.Blocks = append(p.r.Blocks, Block{Type: "activation", Attrs: map[string]any{"administrative_status": s}})
			delete(p.rest, "activation")
		}
	}

	var assignments, inducements []Block
	var archetypeSeen, holderSeen bool
	var unexpressed []any
	for _, v := range asList(p.rest["assignment"]) {
		vm, isMap := v.(map[string]any)
		if !isMap {
			unexpressed = append(unexpressed, v)
			continue
		}
		if !archetypeSeen {
			if oid, ok := pureArchetypeAssignment(vm); ok {
				archetypeSeen = true
				p.r.Attrs["archetype"] = oid
				continue
			}
		}
		if restType == "archetypes" && !holderSeen {
			if names, ok := holderTypeAssignment(vm); ok {
				holderSeen = true
				list := make([]any, len(names))
				for i, n := range names {
					list[i] = n
				}
				p.r.Attrs["holder_type"] = list
				continue
			}
		}
		if b, ok := assignmentToBlock("assignment", vm); ok {
			assignments = append(assignments, b)
		} else {
			unexpressed = append(unexpressed, v)
		}
	}
	setItems(p, "assignment", unexpressed)

	unexpressed = nil
	for _, v := range asList(p.rest["inducement"]) {
		if vm, isMap := v.(map[string]any); isMap {
			if b, ok := assignmentToBlock("inducement", vm); ok {
				inducements = append(inducements, b)
				continue
			}
		}
		unexpressed = append(unexpressed, v)
	}
	setItems(p, "inducement", unexpressed)
	p.r.Blocks = append(p.r.Blocks, assignments...)
	p.r.Blocks = append(p.r.Blocks, inducements...)
}

// setItems puts the values a block cannot express back as the item, so that
// they land in Extra (the provider merges them with the blocks).
func setItems(p *projector, item string, values []any) {
	delete(p.rest, item)
	if len(values) == 0 {
		return
	}
	p.rest[item] = values
	p.note("%s: %d value(s) a block cannot express, kept in extra", item, len(values))
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

// pureArchetypeAssignment returns the archetype oid of an assignment that
// holds nothing but a reference to an archetype by oid (and the default
// relation). Mirrors the provider's pureArchetypeAssignment.
func pureArchetypeAssignment(v map[string]any) (string, bool) {
	if len(v) != 1 {
		return "", false
	}
	ref, ok := v["targetRef"].(map[string]any)
	if !ok {
		return "", false
	}
	oid, _ := ref["oid"].(string)
	typ, _ := ref["type"].(string)
	if oid == "" || strings.TrimPrefix(typ, "c:") != "ArchetypeType" {
		return "", false
	}
	for k, e := range ref {
		switch k {
		case "oid", "type":
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

// holderTypeAssignment returns the holder types of the standard
// assignmentRelation assignment of an archetype (the provider's
// holderTypeAssignment): identifier holderType and one assignmentRelation
// that only holds holderType.
func holderTypeAssignment(v map[string]any) ([]string, bool) {
	if len(v) != 2 || v["identifier"] != "holderType" {
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
		names = append(names, strings.TrimPrefix(strings.TrimPrefix(s, "#"), "c:"))
	}
	return names, len(names) > 0
}

// assignmentToBlock is the inverse of the provider's assignmentToJSON: the
// block for the canonical assignment or inducement v. ok is false when a
// block cannot express v (a target reference beyond oid, type, relation and
// a filter that has a text form, a non-integer order, a condition or policy
// rule that is not an object).
func assignmentToBlock(item string, v map[string]any) (Block, bool) {
	b := Block{Type: item, Attrs: map[string]any{}}
	rest := make(map[string]any, len(v))
	for k, e := range v {
		rest[k] = e
	}
	if tr, present := rest["targetRef"]; present {
		m, isMap := tr.(map[string]any)
		if !isMap {
			return b, false
		}
		t, ok := targetBlock(m)
		if !ok {
			return b, false
		}
		b.Blocks = append(b.Blocks, t)
		delete(rest, "targetRef")
	}
	if o, present := rest["order"]; present {
		n, ok := integer(o)
		if !ok {
			return b, false
		}
		b.Attrs["order"] = n
		delete(rest, "order")
	}
	if c, present := rest["construction"]; present {
		m, isMap := c.(map[string]any)
		if !isMap {
			return b, false
		}
		b.Blocks = append(b.Blocks, constructionBlock(m))
		delete(rest, "construction")
	}
	for item, attr := range map[string]string{"condition": "condition", "policyRule": "policy_rule"} {
		val, present := rest[item]
		if !present {
			continue
		}
		if _, isMap := val.(map[string]any); !isMap {
			return b, false
		}
		b.Attrs[attr] = val
		delete(rest, item)
	}
	if len(rest) > 0 {
		b.Attrs["extra"] = rest
	}
	if len(b.Blocks) == 0 && len(b.Attrs) == 0 {
		return b, false
	}
	return b, true
}

// integer is v as an int when it is a whole number.
func integer(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
			return int(n), true
		}
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// targetBlock is the target block for the canonical reference m: oid, type,
// relation (left out when org:default, as the provider's refToJSON does) and
// a filter, written in the text query form. ok is false for anything else.
func targetBlock(m map[string]any) (Block, bool) {
	b := Block{Type: "target", Attrs: map[string]any{}}
	for k, v := range m {
		switch k {
		case "oid", "type":
			s, ok := v.(string)
			if !ok {
				return b, false
			}
			b.Attrs[k] = s
		case "relation":
			s, ok := v.(string)
			if !ok {
				return b, false
			}
			if s != defaultRelation {
				b.Attrs["relation"] = s
			}
		case "filter":
			text, ok := normalize.FilterText(asMap(v))
			if !ok {
				return b, false
			}
			b.Attrs["filter"] = text
		default:
			return b, false
		}
	}
	_, hasOID := b.Attrs["oid"]
	_, hasFilter := b.Attrs["filter"]
	return b, hasOID || hasFilter
}

// constructionBlock is the construction block for the canonical
// construction m: resource, kind and intent when plain, the rest as body.
func constructionBlock(m map[string]any) Block {
	b := Block{Type: "construction", Attrs: map[string]any{}}
	body := make(map[string]any, len(m))
	for k, v := range m {
		body[k] = v
	}
	if oid, ok := plainResourceRef(m["resourceRef"]); ok {
		b.Attrs["resource"] = oid
		delete(body, "resourceRef")
	}
	for _, k := range []string{"kind", "intent"} {
		if s, ok := m[k].(string); ok {
			b.Attrs[k] = s
			delete(body, k)
		}
	}
	if len(body) > 0 {
		b.Attrs["body"] = body
	}
	return b
}

// plainResourceRef returns the oid of a resource reference the construction
// resource attribute expresses (the provider's plainResourceRef).
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

// asMap is v as a map, nil when it is not one.
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
