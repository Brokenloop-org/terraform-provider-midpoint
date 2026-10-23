package convert

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

// Ref is a Terraform expression that stands for a value, e.g.
// "midpoint_service.dev_k8s.oid" or "data.midpoint_object.archetype_328.oid".
//
// Rule for the renderer: a Ref is written as the bare traversal, never
// quoted. That holds wherever the Ref sits, including nested inside the
// maps and lists of Block.Attrs that are rendered as jsonencode(...): there
// the traversal is an element of the jsonencode argument, e.g.
// jsonencode({ resourceRef = { oid = midpoint_resource.x.oid } }).
type Ref struct{ Address string }

// DataLookup is a data.midpoint_object the output needs for a built-in
// object that is not part of the converted set. Lookup is by OID (the name
// is unknowable without a server, so Name stays empty); RestType is the
// REST collection type of the data source's type attribute.
type DataLookup struct {
	// Label is the unique label among the data lookups.
	Label, RestType, Name, OID string
}

// Address is the traversal to the lookup's oid.
func (d DataLookup) Address() string { return "data.midpoint_object." + d.Label + ".oid" }

// Rewrite replaces the references to converted objects by Refs. rs must
// already have unique labels (see DedupeLabels); the input is not modified.
//
// A reference is a "target" block (oid), the archetype, owner and
// construction resource attributes, and any map under a key ending in "Ref"
// that holds an "oid", wherever it sits in dynamic attributes, extra, block
// free-form maps or an object body. For each:
//
//   - the oid is the object itself: left literal (no dependency cycle);
//   - the oid belongs to a converted object: it becomes a Ref to it (a type
//     that disagrees with the converted kind gives a warning), and a filter
//     is kept;
//   - otherwise with a filter: the oid is dropped and the filter alone
//     remains, midPoint resolves it;
//   - otherwise a built-in oid (00000000-0000-0000-0000-*) becomes a Ref to
//     a data.midpoint_object looked up by oid;
//   - any other oid stays literal and a note reports it.
//
// Notes carry the resource address (e.g. "midpoint_role.vault") as Path.
func Rewrite(rs []Resource) (out []Resource, data []DataLookup, notes []Note) {
	w := &rewriter{byOID: map[string]Resource{}, lookups: map[string]string{}, labels: map[string]bool{}}
	for _, r := range rs {
		if r.OID != "" {
			w.byOID[r.OID] = r
		}
	}
	out = make([]Resource, len(rs))
	for i, r := range rs {
		w.cur = r
		out[i] = w.resource(r)
	}
	return out, w.data, w.notes
}

type rewriter struct {
	byOID   map[string]Resource
	cur     Resource
	lookups map[string]string // oid -> label of its data lookup
	labels  map[string]bool
	data    []DataLookup
	notes   []Note
}

func (w *rewriter) note(format string, args ...any) {
	w.notes = append(w.notes, Note{Path: w.cur.Kind + "." + w.cur.Label, Message: fmt.Sprintf(format, args...)})
}

func (w *rewriter) resource(r Resource) Resource {
	out := r
	out.Attrs = make(map[string]any, len(r.Attrs))
	for _, k := range sortedKeys(r.Attrs) {
		v := r.Attrs[k]
		switch implied := map[string]string{"archetype": "ArchetypeType", "owner": "UserType"}[k]; {
		case implied != "":
			if oid, ok := v.(string); ok {
				if ref, _ := w.reference(oid, implied, false); ref != nil {
					v = ref
				}
			}
		default:
			v = w.value("", v)
		}
		out.Attrs[k] = v
	}
	out.Blocks = w.blocks(r.Blocks)
	if r.Extra != nil {
		out.Extra = w.value("", r.Extra).(map[string]any)
	}
	out.Notes = slices.Clone(r.Notes)
	return out
}

func (w *rewriter) blocks(bs []Block) []Block {
	if bs == nil {
		return nil
	}
	out := make([]Block, len(bs))
	for i, b := range bs {
		nb := Block{Type: b.Type, Attrs: make(map[string]any, len(b.Attrs)), Blocks: w.blocks(b.Blocks)}
		_, hasFilter := b.Attrs["filter"]
		for _, k := range sortedKeys(b.Attrs) {
			v := b.Attrs[k]
			oid, isString := v.(string)
			switch {
			case b.Type == "target" && k == "oid" && isString:
				typ, _ := b.Attrs["type"].(string)
				ref, drop := w.reference(oid, typ, hasFilter)
				if ref != nil {
					v = ref
				} else if drop {
					continue
				}
			case b.Type == "construction" && k == "resource" && isString:
				if ref, _ := w.reference(oid, "ResourceType", false); ref != nil {
					v = ref
				}
			default:
				v = w.value("", v)
			}
			nb.Attrs[k] = v
		}
		out[i] = nb
	}
	return out
}

// value returns a deep copy of v with the references rewritten. key is the
// map key v sits under.
func (w *rewriter) value(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		if strings.HasSuffix(key, "Ref") {
			if oid, ok := t["oid"].(string); ok {
				return w.refMap(oid, t)
			}
		}
		out := make(map[string]any, len(t))
		for _, k := range sortedKeys(t) {
			out[k] = w.value(k, t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = w.value(key, e)
		}
		return out
	default:
		return v
	}
}

// refMap rewrites the reference map m, whose oid is oid.
func (w *rewriter) refMap(oid string, m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for _, k := range sortedKeys(m) {
		out[k] = w.value(k, m[k])
	}
	typ, _ := m["type"].(string)
	_, hasFilter := m["filter"]
	ref, drop := w.reference(oid, typ, hasFilter)
	if ref != nil {
		out["oid"] = ref
	} else if drop {
		delete(out, "oid")
	}
	return out
}

// reference decides what the reference to oid becomes. typ is the type the
// reference claims (empty when it names none). ref is the replacement Ref,
// nil to keep the literal oid; drop asks to remove the oid (a filter
// remains).
func (w *rewriter) reference(oid, typ string, hasFilter bool) (ref any, drop bool) {
	if oid == "" || oid == w.cur.OID {
		return nil, false
	}
	rest, known := restTypeOf(typ)
	if target, ok := w.byOID[oid]; ok {
		if known && target.Kind != kindOf(rest) {
			w.note("reference %s claims type %s but the object is a %s; rewritten by oid", oid, typ, target.Kind)
		}
		return Ref{Address: target.Kind + "." + target.Label + ".oid"}, false
	}
	if hasFilter {
		w.note("reference %s is not converted; its filter alone is kept and the oid dropped", oid)
		return nil, true
	}
	if !strings.HasPrefix(oid, builtinPrefix) {
		w.note("reference %s is not converted and not a built-in object; left as a literal oid", oid)
		return nil, false
	}
	if !known {
		rest = "objects"
	}
	label, ok := w.lookups[oid]
	if !ok {
		label = w.dataLabel(rest, oid)
		w.lookups[oid] = label
		w.data = append(w.data, DataLookup{Label: label, RestType: rest, OID: oid})
	}
	return Ref{Address: "data.midpoint_object." + label + ".oid"}, false
}

// dataLabel is the unique label of a lookup: the singular of the REST type,
// "_", and the oid without its leading zeros (archetype_328).
func (w *rewriter) dataLabel(rest, oid string) string {
	tail := oid[strings.LastIndex(oid, "-")+1:]
	if tail = strings.TrimLeft(tail, "0"); tail == "" {
		tail = "0"
	}
	base := Label(client.SingularOf(rest)+"_"+tail, "")
	l := base
	for n := 2; w.labels[l]; n++ {
		l = fmt.Sprintf("%s_%d", base, n)
	}
	w.labels[l] = true
	return l
}

// kindOf is the resource kind that manages the REST type.
func kindOf(rest string) string {
	if k, ok := kindsByRestType[rest]; ok {
		return k
	}
	return "midpoint_object"
}

// restTypeOf maps a midPoint type name ("c:ArchetypeType", "#RoleType") to
// its REST collection type ("archetypes"). ok is false for no or an unknown
// type.
func restTypeOf(typ string) (rest string, ok bool) {
	typ = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(typ, "#"), "c:"), "Type")
	if typ == "" {
		return "", false
	}
	for _, t := range restTypes {
		if strings.EqualFold(client.SingularOf(t), typ) {
			return t, true
		}
	}
	return "", false
}
