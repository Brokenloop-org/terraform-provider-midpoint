package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
)

// prismTypesNS is the namespace of ProtectedStringType and its items
// (clearValue, encryptedData, externalData, ...).
const prismTypesNS = "http://prism.evolveum.com/xml/ns/public/types-3"

// The connector configuration properties are not defined in midPoint's
// static schema: their definitions come from the connector. The functions
// below change only what is sent for them, never the desired body that is
// compared or stored.
const (
	connectorConfigurationItem  = "connectorConfiguration"
	configurationPropertiesPath = connectorConfigurationItem + "/configurationProperties"
)

// connectorCreateBody returns a copy of body in which every protected string
// under connectorConfiguration carries the types namespace as "@ns". Without
// it the protected string's items inherit the connector namespace of the
// configuration properties, and midPoint 4.11 fails the create with "Cannot
// marshall property value PPV(null)" (an externalData reference) or drops the
// value. A protected string with an "@ns" of its own keeps it. It never
// mutates body.
func connectorCreateBody(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if k == connectorConfigurationItem {
			v = qualifyProtected(v)
		}
		out[k] = v
	}
	return out
}

// qualifyProtected returns a copy of v with "@ns" set on every protected
// string that has none.
func qualifyProtected(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t)+1)
		for k, e := range t {
			out[k] = qualifyProtected(e)
		}
		if isProtectedValue(t) {
			if _, ok := t["@ns"]; !ok {
				out["@ns"] = prismTypesNS
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = qualifyProtected(e)
		}
		return out
	default:
		return v
	}
}

// connectorDeltas returns deltas with every value under
// connectorConfiguration/configurationProperties sent typed. midPoint 4.11
// cannot modify a configuration property given as an untyped value ("An
// attempt to modify an immutable: XNode"), with or without a secret in the
// resource. A configurationProperties value sent whole also gets the "@ns"
// configured in desired (the raw desired body): the deltas are built from the
// compared form, which has none, and midPoint would store the properties
// outside the connector namespace, where the connector finds nothing. Other
// deltas are returned as they are. It never mutates its inputs.
func connectorDeltas(desired map[string]any, deltas []client.ItemDelta) []client.ItemDelta {
	ns := configuredPropertiesNS(desired)
	out := make([]client.ItemDelta, len(deltas))
	for i, d := range deltas {
		path := stripContainerIDs(d.Path)
		switch {
		case path == connectorConfigurationItem:
			d.Value = mapValues(d.Value, func(m map[string]any) any {
				out := make(map[string]any, len(m))
				for k, v := range m {
					if k == "configurationProperties" {
						v = typedProperties(v, ns)
					}
					out[k] = v
				}
				return out
			})
		case path == configurationPropertiesPath:
			d.Value = typedProperties(d.Value, ns)
		case strings.HasPrefix(path, configurationPropertiesPath+"/"):
			d.Value = typedValue(d.Value)
		}
		out[i] = d
	}
	return out
}

// configuredPropertiesNS is the "@ns" of the configured
// connectorConfiguration/configurationProperties, or "".
func configuredPropertiesNS(desired map[string]any) string {
	cc, _ := desired[connectorConfigurationItem].(map[string]any)
	cp, _ := cc["configurationProperties"].(map[string]any)
	ns, _ := cp["@ns"].(string)
	return ns
}

// stripContainerIDs removes the "[id]" parts of a delta path.
func stripContainerIDs(path string) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if j := strings.Index(s, "["); j >= 0 {
			segments[i] = s[:j]
		}
	}
	return strings.Join(segments, "/")
}

// mapValues applies f to v when it is a map, or to each map in v when it is
// a list; anything else is returned as it is.
func mapValues(v any, f func(map[string]any) any) any {
	switch t := v.(type) {
	case map[string]any:
		return f(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			if m, ok := e.(map[string]any); ok {
				out[i] = f(m)
			} else {
				out[i] = e
			}
		}
		return out
	default:
		return v
	}
}

// typedProperties types each configurationProperties value in v (a map or a
// list of maps) and sets its "@ns" to ns when ns is set and it has none.
func typedProperties(v any, ns string) any {
	return mapValues(v, func(m map[string]any) any {
		out := make(map[string]any, len(m)+1)
		for k, e := range m {
			if strings.HasPrefix(k, "@") {
				out[k] = e
			} else {
				out[k] = typedValue(e)
			}
		}
		if _, ok := m["@ns"]; !ok && ns != "" {
			out["@ns"] = ns
		}
		return out
	})
}

// typedValue is v as sent for a configuration property: scalars wrapped with
// an explicit type (numbers as their text, which midPoint converts to the
// property's type), protected strings qualified and typed, other values that
// carry "@type" unchanged, and lists and other maps typed element by element.
func typedValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return map[string]any{"@type": "xsd:boolean", "@value": t}
	case string:
		return map[string]any{"@type": "xsd:string", "@value": t}
	case json.Number:
		return map[string]any{"@type": "xsd:string", "@value": t.String()}
	case int, int64, float64:
		return map[string]any{"@type": "xsd:string", "@value": fmt.Sprint(t)}
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = typedValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t)+2)
		if isProtectedValue(t) {
			// Typed or not, a protected string without the types namespace
			// is stored without its value.
			for k, e := range t {
				out[k] = e
			}
			if _, ok := t["@ns"]; !ok {
				out["@ns"] = prismTypesNS
			}
			if _, ok := t["@type"]; !ok {
				out["@type"] = "t:ProtectedStringType"
			}
			return out
		}
		if _, ok := t["@type"]; ok {
			return t
		}
		for k, e := range t {
			if strings.HasPrefix(k, "@") {
				out[k] = e
			} else {
				out[k] = typedValue(e)
			}
		}
		return out
	default:
		return v
	}
}

// isProtectedValue reports whether m is a protected string value.
func isProtectedValue(m map[string]any) bool {
	for _, k := range protectedKeys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}
