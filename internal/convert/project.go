package convert

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Resource is one object of the catalogue projected onto a resource of the
// midpoint provider. It is the exact inverse of the provider's model to body
// mapping, so that the generated configuration imports and plans clean.
//
// # Value rules for the renderer
//
// Attrs hold the resource's own attributes. A string, bool, integer or
// float64 is a scalar. A map[string]any or []any is a dynamic or nested
// attribute (extension, authorization, connector, schema_handling, ...) and
// is rendered as an HCL object or tuple.
//
// Block.Attrs hold the attributes of a block. There the rule is different:
// the provider's block schema has no dynamic attributes, so every attribute
// that holds a map[string]any or []any (condition, policy_rule, extra and
// construction body) is JSON text, and the renderer must write it as
// jsonencode(...). Scalars are written as HCL scalars.
type Resource struct {
	// Kind is the resource type, e.g. "midpoint_role".
	Kind string
	// Label is the base Terraform label, see Label; DedupeLabels makes the
	// labels of a set unique.
	Label string
	// OID is the object's oid, empty when it has none.
	OID string
	// Attrs maps HCL attribute names to values (the oid is OID, not an
	// attribute here).
	Attrs map[string]any
	// Blocks are the nested blocks, in output order.
	Blocks []Block
	// Extra holds the root items no typed attribute claims, in midPoint
	// naming. It is the extra attribute; nil when empty.
	Extra map[string]any
	// AdoptExisting is set for midPoint built-ins (oid 00000000-0000-0000-
	// 0000-*), which exist before Terraform creates them.
	AdoptExisting bool
	// Notes report what the projection did that the user may want to know,
	// e.g. the items that went to Extra.
	Notes []string
}

// Block is a nested block: Attrs follow the block rule of Resource.
type Block struct {
	Type   string
	Attrs  map[string]any
	Blocks []Block
}

// builtinPrefix is the oid range of the objects midPoint ships.
const builtinPrefix = "00000000-0000-0000-0000-"

// kindsByRestType maps a REST type to the typed resource that manages it.
var kindsByRestType = map[string]string{
	"roles": "midpoint_role", "services": "midpoint_service", "orgs": "midpoint_org",
	"archetypes": "midpoint_archetype", "resources": "midpoint_resource", "tasks": "midpoint_task",
	"objectTemplates": "midpoint_object_template", "marks": "midpoint_mark",
}

// Project maps s onto the provider resource that manages its REST type; a
// type without a typed resource becomes a midpoint_object. Items no
// attribute or block claims go to Extra, and are listed in Notes.
func Project(s Source) (Resource, error) {
	if s.Body == nil {
		return Resource{}, errors.New("source has no body")
	}
	body, _ := stripIDs(s.Body).(map[string]any)
	oid := s.OID
	if o, ok := body["oid"].(string); ok && oid == "" {
		oid = o
	}
	delete(body, "oid")
	name := s.Name
	if n, ok := body["name"].(string); ok && name == "" {
		name = n
	}

	r := Resource{Kind: kindsByRestType[s.RestType], OID: oid, Attrs: map[string]any{}}
	r.Label = Label(name, oid)
	r.AdoptExisting = strings.HasPrefix(oid, builtinPrefix)
	p := &projector{r: &r, rest: body}

	switch s.RestType {
	case "roles", "services", "orgs", "archetypes":
		p.focus(s.RestType)
	case "resources":
		p.resource()
	case "tasks":
		p.task()
	case "objectTemplates":
		p.simple(simpleMap{
			strings:  map[string]string{"name": "name", "description": "description", "lifecycleState": "lifecycle_state"},
			dynamics: map[string]string{"mapping": "mapping", "item": "item", "iterationSpecification": "iteration_specification"},
		})
	case "marks":
		p.simple(simpleMap{
			strings:  map[string]string{"name": "name", "description": "description"},
			dynamics: map[string]string{"display": "display", "eventMark": "event"},
		})
	default:
		r.Kind = "midpoint_object"
		// The name attribute of midpoint_object is computed from body, so
		// body keeps the name; only the oid moves to the oid attribute.
		r.Attrs["type"] = s.RestType
		r.Attrs["body"] = body
		p.rest = nil
	}
	p.finish()
	return r, nil
}

// projector moves items from rest, the not yet claimed root items, to the
// attributes and blocks of r.
type projector struct {
	r    *Resource
	rest map[string]any
}

// simpleMap maps root items to typed attributes: string items to string
// attributes, others to dynamic attributes.
type simpleMap struct {
	strings, dynamics map[string]string
}

// takeString claims the string item and stores it as attr.
func (p *projector) takeString(item, attr string) {
	if v, ok := p.rest[item].(string); ok {
		p.r.Attrs[attr] = v
		delete(p.rest, item)
	}
}

// takeBool claims the bool item and stores it as attr.
func (p *projector) takeBool(item, attr string) {
	if v, ok := p.rest[item].(bool); ok {
		p.r.Attrs[attr] = v
		delete(p.rest, item)
	}
}

// takeDynamic claims the item whatever its value and stores it as attr; a
// null item is dropped.
func (p *projector) takeDynamic(item, attr string) {
	v, ok := p.rest[item]
	if !ok {
		return
	}
	delete(p.rest, item)
	if v != nil {
		p.r.Attrs[attr] = v
	}
}

// takeAll claims every item of m.
func (p *projector) takeAll(m simpleMap) {
	for _, item := range sortedKeys(m.strings) {
		p.takeString(item, m.strings[item])
	}
	for _, item := range sortedKeys(m.dynamics) {
		p.takeDynamic(item, m.dynamics[item])
	}
}

func (p *projector) simple(m simpleMap) { p.takeAll(m) }

// note adds a note to the resource.
func (p *projector) note(format string, args ...any) {
	p.r.Notes = append(p.r.Notes, fmt.Sprintf(format, args...))
}

// finish moves what is left to Extra.
func (p *projector) finish() {
	for k, v := range p.rest {
		if v == nil {
			continue
		}
		if p.r.Extra == nil {
			p.r.Extra = map[string]any{}
		}
		p.r.Extra[k] = v
	}
	if len(p.r.Extra) > 0 {
		p.note("extra: %s", strings.Join(sortedKeys(p.r.Extra), ", "))
	}
}

// stripIDs returns a deep copy of v without container "@id"s (server
// identifiers, never configuration; the provider's state omits them).
func stripIDs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if k != "@id" {
				out[k] = stripIDs(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = stripIDs(e)
		}
		return out
	default:
		return v
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// Label is the base Terraform label of an object: the slug of its name, or
// of its oid when it has no name. The slug is lowercase, with every run of
// characters other than ASCII letters and digits turned into one "_",
// trimmed of "_", and prefixed with "_" when it starts with a digit.
// DedupeLabels makes labels unique over a set.
func Label(name, oid string) string {
	if l := slug(name); l != "" {
		return l
	}
	if l := slug(strings.ReplaceAll(oid, "-", "_")); l != "" {
		return l
	}
	return "object"
}

func slug(s string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if underscore && b.Len() > 0 {
				b.WriteByte('_')
			}
			underscore = false
			b.WriteRune(r)
		} else {
			underscore = true
		}
	}
	out := b.String()
	if out != "" && out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

// DedupeLabels makes the labels of rs unique over the whole set, in order:
// a label already taken gets the suffix _2, _3, ... .
func DedupeLabels(rs []Resource) {
	taken := make(map[string]bool, len(rs))
	for i := range rs {
		base := rs[i].Label
		l := base
		for n := 2; taken[l]; n++ {
			l = fmt.Sprintf("%s_%d", base, n)
		}
		taken[l] = true
		rs[i].Label = l
	}
}
