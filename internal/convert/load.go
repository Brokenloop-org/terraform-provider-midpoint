// Package convert turns a midPoint object catalogue (one JSON, YAML or XML
// file per object) into configuration for the midpoint Terraform provider.
// It never contacts a server.
package convert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// Source is one object read from the catalogue.
type Source struct {
	// Path is the file the object was read from.
	Path string
	// RestType is the REST collection type, e.g. "roles".
	RestType string
	// OID is the object's oid, empty when the file has none.
	OID string
	// Name is the object's name, empty when it has none.
	Name string
	// Body is the object without its wrapper, in normalize.Canonical form.
	Body map[string]any
}

// Note reports something Load did that the user may want to know: a skipped
// file, a duplicate, or server-managed items dropped from an object.
type Note struct {
	Path    string
	Message string
}

// ConflictError reports two files that hold different objects with the same
// OID.
type ConflictError struct {
	OID          string
	PathA, PathB string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("oid %s is defined differently in %s and %s", e.OID, e.PathA, e.PathB)
}

// restTypes are the REST collection types an object file can hold.
var restTypes = []string{
	"accessCertificationCampaigns", "accessCertificationDefinitions", "archetypes", "connectorHosts",
	"connectors", "dashboards", "forms", "functionLibraries", "genericObjects", "lookupTables",
	"marks", "nodes", "objectCollections", "objectTemplates", "orgs", "policies", "reports",
	"resources", "roles", "securityPolicies", "services", "shadows", "systemConfigurations",
	"tasks", "users", "valuePolicies",
}

// wrapperTypes maps a wrapper key ("role") to its REST type ("roles").
var wrapperTypes = func() map[string]string {
	m := make(map[string]string, len(restTypes))
	for _, t := range restTypes {
		m[client.SingularOf(t)] = t
	}
	return m
}()

// Load reads every object file below roots (directories or single files) in
// path order. Files that are not objects (templates, other extensions, an
// unknown or missing wrapper key) are skipped with a Note; a JSON, YAML or
// XML file that cannot be parsed is an error, so a typo never silently drops
// an object. Files with
// the same OID and identical canonical content yield one Source; differing
// content is a *ConflictError.
func Load(roots []string) ([]Source, []Note, error) {
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}

	var (
		srcs  []Source
		notes []Note
		byOID = map[string]int{}
	)
	for _, p := range files {
		src, dropped, skip, err := loadFile(p)
		if err != nil {
			return nil, nil, err
		}
		if skip != "" {
			notes = append(notes, Note{p, "skipped: " + skip})
			continue
		}
		if i, seen := byOID[src.OID]; src.OID != "" && seen {
			prev := srcs[i]
			if prev.RestType != src.RestType || !reflect.DeepEqual(prev.Body, src.Body) {
				return nil, nil, &ConflictError{OID: src.OID, PathA: prev.Path, PathB: p}
			}
			notes = append(notes, Note{p, "duplicate of " + prev.Path + " (identical content), ignored"})
			continue
		}
		if len(dropped) > 0 {
			notes = append(notes, Note{p, "dropped server-managed items: " + strings.Join(dropped, ", ")})
		}
		if src.OID != "" {
			byOID[src.OID] = len(srcs)
		}
		srcs = append(srcs, src)
	}
	return srcs, notes, nil
}

// loadFile reads one file. A non-empty skip is the reason the file is not an
// object file; err is reserved for failures that make the load unusable.
func loadFile(p string) (src Source, dropped []string, skip string, err error) {
	base := strings.ToLower(filepath.Base(p))
	if strings.HasSuffix(base, ".tmpl.json") {
		return src, nil, "template file, not an object", nil
	}
	var decode func([]byte) (map[string]any, error)
	switch strings.ToLower(filepath.Ext(p)) {
	case ".json":
		decode = decodeJSON
	case ".yaml", ".yml":
		decode = decodeYAML
	case ".xml":
		decode = decodeXML
	default:
		return src, nil, "not a JSON, YAML or XML file", nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return src, nil, "", err
	}
	doc, err := decode(data)
	if err != nil {
		return src, nil, "", fmt.Errorf("%s: cannot parse: %w", p, err)
	}
	if len(doc) != 1 {
		return src, nil, "not a single wrapped object", nil
	}
	for key, v := range doc {
		restType, ok := wrapperTypes[key]
		body, isMap := v.(map[string]any)
		if !ok || !isMap {
			return src, nil, fmt.Sprintf("top-level key %q is not a known object type", key), nil
		}
		src = Source{Path: p, RestType: restType, Body: normalize.Canonical(restType, body)}
		dropped = droppedItems(body, src.Body)
	}
	src.OID, _ = src.Body["oid"].(string)
	src.Name, _ = src.Body["name"].(string)
	return src, dropped, "", nil
}

// stripPrefix removes the namespace prefix normalize.Canonical strips from
// item names.
func stripPrefix(k string) string {
	for _, p := range []string{"c:", "t:", "q:"} {
		if strings.HasPrefix(k, p) {
			return k[len(p):]
		}
	}
	return k
}

// canonicalPaths collects the item paths (names only, no list indexes) of v.
// The value is true when the item holds a container, whose children are
// listed too.
func canonicalPaths(path []string, v any, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			p := append(slices.Clip(path), k)
			_, isMap := child.(map[string]any)
			_, isList := child.([]any)
			out[strings.Join(p, "/")] = isMap || isList
			canonicalPaths(p, child, out)
		}
	case []any:
		for _, e := range t {
			canonicalPaths(path, e, out)
		}
	}
}

// droppedItems lists the items present in raw but absent from canon, the
// output of normalize.Canonical for raw. Diffing the two keeps the notes in
// step with Canonical whatever it drops. Only the topmost missing item is
// listed, extended down a chain of single-key maps (a container Canonical
// pruned empty is reported by what it held, e.g. "capabilities/native").
func droppedItems(raw, canon map[string]any) []string {
	keep := map[string]bool{}
	canonicalPaths(nil, canon, keep)
	var out []string
	var walk func(path []string, v any)
	walk = func(path []string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				p := append(slices.Clip(path), stripPrefix(k))
				container, kept := keep[strings.Join(p, "/")]
				switch {
				case !kept:
					out = append(out, strings.Join(chain(p, child), "/"))
				case container:
					walk(p, child)
				}
			}
		case []any:
			for _, e := range t {
				walk(path, e)
			}
		}
	}
	walk(nil, raw)
	slices.Sort(out)
	return slices.Compact(out)
}

// chain extends path through v while v is a map with a single key.
func chain(path []string, v any) []string {
	for {
		m, ok := v.(map[string]any)
		if !ok || len(m) != 1 {
			return path
		}
		for k, child := range m {
			path = append(slices.Clip(path), stripPrefix(k))
			v = child
		}
	}
}

func decodeJSON(data []byte) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// decodeYAML decodes YAML through JSON so numbers and nesting have exactly
// the shape decodeJSON gives.
func decodeYAML(data []byte) (map[string]any, error) {
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(bytes.TrimSpace(b))
}
