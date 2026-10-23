package convert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"

	hcljson "github.com/hashicorp/hcl/v2/json"
)

// jsonRenderer writes Terraform JSON syntax (.tf.json). Every JSON string is
// a template there, so literal "${" and "%{" are escaped; a Ref is a
// "${...}" interpolation. A free-form block attribute is the JSON text of
// its value, or "${jsonencode(...)}" when it holds a Ref (both evaluate to
// the same string).
type jsonRenderer struct{}

func (jsonRenderer) ext() string { return ".tf.json" }

func (jsonRenderer) resources(docs []resourceDoc) ([]byte, error) {
	byKind := map[string]any{}
	for _, d := range docs {
		body := map[string]any{}
		if len(d.comments) > 0 {
			body["//"] = strings.Join(d.comments, " ")
		}
		for _, a := range d.attrs {
			v, err := jsonValue(a.value)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %s: %w", d.kind, d.label, a.name, err)
			}
			body[a.name] = v
		}
		if err := jsonBlocks(body, d.blocks); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", d.kind, d.label, err)
		}
		labels, _ := byKind[d.kind].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
			byKind[d.kind] = labels
		}
		labels[d.label] = body
	}
	return encodeJSON(map[string]any{"resource": byKind})
}

func (jsonRenderer) data(list []DataLookup) ([]byte, error) {
	labels := map[string]any{}
	for _, d := range list {
		labels[d.Label] = map[string]any{"type": escapeTemplate(d.RestType), "oid": escapeTemplate(d.OID)}
	}
	return encodeJSON(map[string]any{"data": map[string]any{"midpoint_object": labels}})
}

func (jsonRenderer) imports(list []importDoc) ([]byte, error) {
	var out []any
	for _, im := range list {
		if !validTraversal(im.to) {
			return nil, fmt.Errorf("invalid import address %q", im.to)
		}
		out = append(out, map[string]any{"to": im.to, "id": escapeTemplate(im.id)})
	}
	return encodeJSON(map[string]any{"import": out})
}

// jsonBlocks adds blocks to body: a list block type as an array of
// objects, a single block as an object.
func jsonBlocks(body map[string]any, blocks []Block) error {
	for _, b := range blocks {
		content := map[string]any{}
		for k, v := range b.Attrs {
			var err error
			if isFreeForm(v) {
				content[k], err = jsonFreeForm(v)
			} else {
				content[k], err = jsonValue(v)
			}
			if err != nil {
				return fmt.Errorf("%s: %s: %w", b.Type, k, err)
			}
		}
		if err := jsonBlocks(content, b.Blocks); err != nil {
			return fmt.Errorf("%s: %w", b.Type, err)
		}
		prev, exists := body[b.Type]
		switch {
		case listBlocks[b.Type]:
			list, _ := prev.([]any)
			if exists && list == nil {
				return fmt.Errorf("%s is both an attribute and a block", b.Type)
			}
			body[b.Type] = append(list, content)
		case exists:
			return fmt.Errorf("%s: a single block given more than once", b.Type)
		default:
			body[b.Type] = content
		}
	}
	return nil
}

// jsonFreeForm is the string a free-form block attribute holds.
func jsonFreeForm(v any) (string, error) {
	if containsRef(v) {
		w := &hclWriter{inline: true}
		if err := w.call("jsonencode", v, 0); err != nil {
			return "", err
		}
		return "${" + w.b.String() + "}", nil
	}
	if err := checkJSON(v); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return escapeTemplate(strings.TrimSuffix(buf.String(), "\n")), nil
}

// jsonValue is v as a Terraform JSON expression value.
func jsonValue(v any) (any, error) {
	switch t := v.(type) {
	case string:
		if !utf8.ValidString(t) {
			return nil, fmt.Errorf("string %q is not valid UTF-8", t)
		}
		return escapeTemplate(t), nil
	case Ref:
		if !validTraversal(t.Address) {
			return nil, fmt.Errorf("invalid reference %q", t.Address)
		}
		return "${" + t.Address + "}", nil
	case expr:
		return "${" + t.text + "}", nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if !utf8.ValidString(k) {
				return nil, fmt.Errorf("key %q is not valid UTF-8", k)
			}
			j, err := jsonValue(e)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[escapeTemplate(k)] = j
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			j, err := jsonValue(e)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = j
		}
		return out, nil
	default:
		if err := checkJSON(v); err != nil {
			return nil, err
		}
		return v, nil
	}
}

// checkJSON fails on values the value rules do not allow.
func checkJSON(v any) error {
	switch t := v.(type) {
	case nil, bool, int, int64, json.Number:
		return nil
	case string:
		if !utf8.ValidString(t) {
			return fmt.Errorf("string %q is not valid UTF-8", t)
		}
		return nil
	case float64:
		if math.IsInf(t, 0) || math.IsNaN(t) {
			return fmt.Errorf("number %v has no JSON form", t)
		}
		return nil
	case map[string]any:
		for k, e := range t {
			if err := checkJSON(e); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		return nil
	case []any:
		for i, e := range t {
			if err := checkJSON(e); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported value of type %s", reflect.TypeOf(v))
	}
}

// encodeJSON writes v indented (keys sorted) and checks that Terraform's
// JSON parser reads it.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if _, diags := hcljson.Parse(buf.Bytes(), "generated.tf.json"); diags.HasErrors() {
		return nil, fmt.Errorf("generated JSON does not parse: %s", diags.Error())
	}
	return buf.Bytes(), nil
}
