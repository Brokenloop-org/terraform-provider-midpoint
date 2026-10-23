package convert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// splitDynamic are the free-form attributes of midpoint_resource that
// SplitFiles moves to YAML files when they are large.
var splitDynamic = []string{"capabilities", "connector_configuration", "extra", "schema_handling", "synchronization"}

// splitResource moves the large free-form attributes in all, the attributes
// of the midpoint_resource labelled label, to YAML files and its schema
// definition to an XSD file, adding the files to side. An attribute that
// holds a reference stays inline (a YAML file cannot hold an expression);
// the returned comments say so.
func splitResource(label string, all map[string]any, side map[string][]byte) (comments []string, err error) {
	for _, name := range splitDynamic {
		v, ok := all[name]
		if !ok || !isFreeForm(v) {
			continue
		}
		w := &hclWriter{}
		w.b.WriteString(name + " = ")
		if err := w.value(v, 1); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if strings.Count(w.b.String(), "\n")+1 <= splitLines {
			continue
		}
		path := label + "/" + name + ".yaml"
		if containsRef(v) {
			comments = append(comments, fmt.Sprintf("%s holds a Terraform reference, so it stays here instead of in %s.", name, path))
			continue
		}
		content, err := yamlFile(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		side[path] = content
		all[name] = expr{fmt.Sprintf(`yamldecode(file("${path.module}/%s"))`, path)}
	}
	if sch, ok := all["schema"].(map[string]any); ok {
		if def, ok := sch["definition"].(string); ok {
			path := label + "/schema.xsd"
			side[path] = []byte(def)
			sch = maps.Clone(sch)
			sch["definition"] = expr{fmt.Sprintf(`file("${path.module}/%s")`, path)}
			all["schema"] = sch
		}
	}
	return comments, nil
}

// yamlFile writes v as YAML (keys sorted, two-space indentation) and checks
// that it reads back as v.
func yamlFile(v any) ([]byte, error) {
	n, err := yamlNode(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	var back any
	if err := yaml.Unmarshal(buf.Bytes(), &back); err != nil {
		return nil, fmt.Errorf("generated YAML does not parse: %w", err)
	}
	if !sameJSON(back, v) {
		return nil, fmt.Errorf("generated YAML does not read back as the value")
	}
	return buf.Bytes(), nil
}

// yamlNode is the YAML node for v: strings are always strings, numbers
// keep their text.
func yamlNode(v any) (*yaml.Node, error) {
	scalar := func(tag, value string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	}
	number := func(text string) *yaml.Node {
		if strings.ContainsAny(text, ".eE") {
			return scalar("!!float", text)
		}
		return scalar("!!int", text)
	}
	switch t := v.(type) {
	case nil:
		return scalar("!!null", "null"), nil
	case string:
		n := scalar("!!str", t)
		if yaml11Ambiguous(t) {
			n.Style = yaml.DoubleQuotedStyle
		}
		return n, nil
	case bool:
		return scalar("!!bool", strconv.FormatBool(t)), nil
	case int:
		return number(strconv.Itoa(t)), nil
	case int64:
		return number(strconv.FormatInt(t, 10)), nil
	case float64:
		return number(strconv.FormatFloat(t, 'g', -1, 64)), nil
	case json.Number:
		if _, err := strconv.ParseFloat(string(t), 64); err != nil {
			return nil, fmt.Errorf("invalid number %q", t)
		}
		return number(string(t)), nil
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range sortedKeys(t) {
			e, err := yamlNode(t[k])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			key := scalar("!!str", k)
			if yaml11Ambiguous(k) {
				key.Style = yaml.DoubleQuotedStyle
			}
			n.Content = append(n.Content, key, e)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for i, e := range t {
			c, err := yamlNode(e)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			n.Content = append(n.Content, c)
		}
		return n, nil
	default:
		return nil, fmt.Errorf("unsupported value of type %s", reflect.TypeOf(v))
	}
}

// sameJSON reports whether a and b encode to the same JSON value.
func sameJSON(a, b any) bool {
	norm := func(v any) (any, bool) {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var out any
		return out, json.Unmarshal(data, &out) == nil
	}
	na, okA := norm(a)
	nb, okB := norm(b)
	return okA && okB && reflect.DeepEqual(na, nb)
}

// yaml11Ambiguous reports whether the plain scalar s, a value or a map
// key, could read as something other than a plain string to a YAML 1.1
// parser (Terraform's yamldecode follows 1.1 in places yaml.v3 does not): a
// bool word such as yes or off, the merge key << or the value key =, or
// anything that starts like a number, a timestamp or a special float. Such
// strings are written double-quoted.
func yaml11Ambiguous(s string) bool {
	switch strings.ToLower(s) {
	case "", "y", "yes", "n", "no", "on", "off", "true", "false", "null", "~", "<<", "=":
		return true
	}
	return strings.ContainsRune("0123456789+-.", rune(s[0]))
}
