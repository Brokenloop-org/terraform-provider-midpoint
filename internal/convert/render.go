package convert

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Options control Render.
type Options struct {
	// Format is "hcl" (the default when empty) or "json", Terraform JSON
	// syntax in .tf.json files.
	Format string
	// SplitFiles moves the large free-form attributes of midpoint_resource
	// to YAML files and every resource schema definition to an .xsd file,
	// next to the configuration.
	SplitFiles bool
	// OutDir is the directory the caller writes the files to. The paths
	// Render returns are relative to it; Render itself does not use it.
	OutDir string
}

// kindFiles maps a resource kind to the file (without extension) that holds
// its resources.
var kindFiles = map[string]string{
	"midpoint_service": "services", "midpoint_role": "roles", "midpoint_org": "orgs",
	"midpoint_archetype": "archetypes", "midpoint_object_template": "object_templates",
	"midpoint_mark": "marks", "midpoint_task": "tasks", "midpoint_resource": "resources",
	"midpoint_object": "objects",
}

// listBlocks are the block types that repeat (list nested blocks); every
// other block type is a single nested block.
var listBlocks = map[string]bool{"assignment": true, "inducement": true}

// splitLines is the size above which SplitFiles moves an attribute to a
// YAML file: its rendered HCL has more lines than this.
const splitLines = 20

// Render writes rs and data as Terraform configuration: one file per kind
// (services.tf, roles.tf, ...), data.tf for the lookups and imports.tf with
// an import block per resource that has an oid. A file that would be empty
// is left out. The result maps paths relative to the output directory to
// their content; with SplitFiles it also holds the YAML and XSD side files.
//
// Values follow the rules documented on Resource and Ref: root-level maps
// and lists are HCL object and tuple literals, maps and lists in block
// attributes are jsonencode(...) expressions, a Ref is a bare traversal
// wherever it sits. Strings are written so that they evaluate to exactly the
// input: "${" and "%{" are escaped, multi-line strings are heredocs.
func Render(rs []Resource, data []DataLookup, o Options) (map[string][]byte, error) {
	var r renderer
	switch o.Format {
	case "", "hcl":
		r = hclRenderer{}
	case "json":
		r = jsonRenderer{}
	default:
		return nil, fmt.Errorf("unknown format %q, want hcl or json", o.Format)
	}

	files := map[string][]byte{}
	byFile := map[string][]Resource{}
	for _, res := range rs {
		f, ok := kindFiles[res.Kind]
		if !ok {
			return nil, fmt.Errorf("%s.%s: unknown resource kind", res.Kind, res.Label)
		}
		if res.Kind == "midpoint_object" && len(res.Extra) > 0 {
			return nil, fmt.Errorf("midpoint_object.%s: extra is not an attribute of midpoint_object; its items belong in body", res.Label)
		}
		byFile[f] = append(byFile[f], res)
	}

	for _, f := range sortedKeys(byFile) {
		list := slices.Clone(byFile[f])
		slices.SortFunc(list, func(a, b Resource) int { return strings.Compare(a.Label, b.Label) })
		var docs []resourceDoc
		for i, res := range list {
			if i > 0 && list[i-1].Label == res.Label {
				return nil, fmt.Errorf("%s.%s: duplicate label", res.Kind, res.Label)
			}
			d, side, err := prepare(res, o.SplitFiles)
			if err != nil {
				return nil, err
			}
			for p, content := range side {
				files[p] = content
			}
			docs = append(docs, d)
		}
		out, err := r.resources(docs)
		if err != nil {
			return nil, err
		}
		files[f+r.ext()] = out
	}

	if len(data) > 0 {
		list := slices.Clone(data)
		slices.SortFunc(list, func(a, b DataLookup) int { return strings.Compare(a.Label, b.Label) })
		out, err := r.data(list)
		if err != nil {
			return nil, err
		}
		files["data"+r.ext()] = out
	}

	var imports []importDoc
	for _, res := range rs {
		if res.OID == "" {
			continue // nothing to import: the object is created
		}
		id := res.OID
		if res.Kind == "midpoint_object" {
			typ, _ := res.Attrs["type"].(string)
			id = typ + "/" + res.OID
		}
		imports = append(imports, importDoc{to: res.Kind + "." + res.Label, id: id})
	}
	if len(imports) > 0 {
		slices.SortFunc(imports, func(a, b importDoc) int { return strings.Compare(a.to, b.to) })
		out, err := r.imports(imports)
		if err != nil {
			return nil, err
		}
		files["imports"+r.ext()] = out
	}
	return files, nil
}

// renderer writes one syntax.
type renderer interface {
	ext() string
	resources([]resourceDoc) ([]byte, error)
	data([]DataLookup) ([]byte, error)
	imports([]importDoc) ([]byte, error)
}

type importDoc struct{ to, id string }

// resourceDoc is a resource ready to write: its attributes in output order.
type resourceDoc struct {
	kind, label string
	attrs       []attr
	blocks      []Block
	// comments are written above the attributes.
	comments []string
}

// attr is an attribute and its value. The value follows the value rules of
// Resource and may also hold Ref and expr values.
type attr struct {
	name  string
	value any
}

// expr is a Terraform expression written as is, e.g. file("${path.module}/x").
type expr struct{ text string }

// headOrder is the order of the leading attributes of a resource; the
// others follow alphabetically.
var headOrder = []string{
	"type", "name", "oid", "display_name", "description", "identifier", "lifecycle_state", "risk_level",
	"requestable", "delegable", "tenant", "archetype", "holder_type", "owner", "binding", "recurrence",
	"initial_execution_state", "connector",
}

// blockAttrOrder is the order of the leading attributes of a block.
var blockAttrOrder = []string{
	"order", "oid", "type", "relation", "filter", "resource", "kind", "intent", "administrative_status",
}

// orderedNames returns the names of m: first those of head in that order,
// then the rest alphabetically.
func orderedNames[V any](m map[string]V, head []string) []string {
	var out []string
	for _, k := range head {
		if _, ok := m[k]; ok {
			out = append(out, k)
		}
	}
	for _, k := range sortedKeys(m) {
		if !slices.Contains(head, k) {
			out = append(out, k)
		}
	}
	return out
}

// prepare orders the attributes of res (oid, adopt_existing and extra
// included) and, with split, moves large free-form attributes of a
// midpoint_resource to side files.
func prepare(res Resource, split bool) (resourceDoc, map[string][]byte, error) {
	d := resourceDoc{kind: res.Kind, label: res.Label, blocks: res.Blocks}
	all := maps.Clone(res.Attrs)
	if all == nil {
		all = map[string]any{}
	}
	for _, reserved := range []string{"oid", "adopt_existing", "extra"} {
		if _, ok := all[reserved]; ok {
			return d, nil, fmt.Errorf("%s.%s: %s must not be in Attrs", res.Kind, res.Label, reserved)
		}
	}
	if res.OID != "" {
		all["oid"] = res.OID
	}
	if len(res.Extra) > 0 {
		all["extra"] = res.Extra
	}
	side := map[string][]byte{}
	if split && res.Kind == "midpoint_resource" {
		var err error
		if d.comments, err = splitResource(res.Label, all, side); err != nil {
			return d, nil, fmt.Errorf("%s.%s: %w", res.Kind, res.Label, err)
		}
	}
	for _, k := range orderedNames(all, headOrder) {
		if k != "extra" {
			d.attrs = append(d.attrs, attr{k, all[k]})
		}
	}
	if res.AdoptExisting {
		d.attrs = append(d.attrs, attr{"adopt_existing", true})
	}
	if v, ok := all["extra"]; ok {
		d.attrs = append(d.attrs, attr{"extra", v})
	}
	return d, side, nil
}

// escapeTemplate escapes the template sequences of s: "${" becomes "$${"
// and "%{" becomes "%%{" (HCL quoted strings, heredocs and Terraform JSON
// strings are all templates).
func escapeTemplate(s string) string {
	if !strings.Contains(s, "${") && !strings.Contains(s, "%{") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteByte(s[i])
		if (s[i] == '$' || s[i] == '%') && i+1 < len(s) && s[i+1] == '{' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// containsRef reports whether v holds a Ref or an expr.
func containsRef(v any) bool {
	switch t := v.(type) {
	case Ref, expr:
		return true
	case map[string]any:
		for _, e := range t {
			if containsRef(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if containsRef(e) {
				return true
			}
		}
	}
	return false
}

// isFreeForm reports whether v is a map or list value.
func isFreeForm(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}
