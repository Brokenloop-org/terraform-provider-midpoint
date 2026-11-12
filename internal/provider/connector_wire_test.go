package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
)

// wireJSON decodes s with JSON numbers kept as json.Number, the form the
// provider's bodies use.
func wireJSON(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decoding %s: %v", s, err)
	}
	return v
}

func wireMap(t *testing.T, s string) map[string]any {
	t.Helper()
	m, ok := wireJSON(t, s).(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", s)
	}
	return m
}

func TestConnectorCreateBody(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{
			name: "protected strings inside the connector configuration are qualified",
			in: `{"name":"r","connectorConfiguration":{"configurationProperties":{"@ns":"urn:c","host":"h",
				"bindPassword":{"externalData":{"provider":"env","key":"K"}},"list":[{"clearValue":"x"}]}}}`,
			want: `{"name":"r","connectorConfiguration":{"configurationProperties":{"@ns":"urn:c","host":"h",
				"bindPassword":{"@ns":"` + prismTypesNS + `","externalData":{"provider":"env","key":"K"}},
				"list":[{"@ns":"` + prismTypesNS + `","clearValue":"x"}]}}}`,
		},
		{
			name: "an @ns already set is kept",
			in:   `{"connectorConfiguration":{"configurationProperties":{"p":{"@ns":"urn:own","clearValue":"x"}}}}`,
			want: `{"connectorConfiguration":{"configurationProperties":{"p":{"@ns":"urn:own","clearValue":"x"}}}}`,
		},
		{
			name: "protected strings elsewhere are not touched",
			in:   `{"extension":{"p":{"clearValue":"x"}}}`,
			want: `{"extension":{"p":{"clearValue":"x"}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := wireMap(t, tc.in)
			before := wireMap(t, tc.in)
			got := connectorCreateBody(in)
			if want := wireMap(t, tc.want); !reflect.DeepEqual(got, want) {
				t.Errorf("got  %s\nwant %s", mustJSON(t, got), mustJSON(t, want))
			}
			if !reflect.DeepEqual(in, before) {
				t.Errorf("input mutated: %s", mustJSON(t, in))
			}
		})
	}
}

func TestConnectorDeltas(t *testing.T) {
	const cp = "connectorConfiguration/configurationProperties"
	str := func(s string) string { return `{"@type":"xsd:string","@value":"` + s + `"}` }
	secret := `{"@ns":"` + prismTypesNS + `","@type":"t:ProtectedStringType","externalData":{"provider":"env","key":"K"}}`
	withNS := `{"connectorConfiguration":{"configurationProperties":{"@ns":"urn:c","host":"h"}}}`
	withoutNS := `{"connectorConfiguration":{"configurationProperties":{"host":"h"}}}`
	tests := []struct {
		name, desired, mod, path, value, want string
	}{
		{"string", withNS, "replace", cp + "/host", `"h2"`, str("h2")},
		{"number as text", withNS, "replace", cp + "/port", `636`, str("636")},
		{"boolean", withNS, "add", cp + "/allowUntrustedSsl", `[true]`, `[{"@type":"xsd:boolean","@value":true}]`},
		{"list", withNS, "replace", cp + "/baseContext", `["a","b"]`, `[` + str("a") + `,` + str("b") + `]`},
		{"secret", withNS, "replace", cp + "/bindPassword", `{"externalData":{"provider":"env","key":"K"}}`, secret},
		{"secret in a list", withNS, "add", cp + "/bindPassword", `[{"externalData":{"provider":"env","key":"K"}}]`, `[` + secret + `]`},
		{"already typed", withNS, "replace", cp + "/host", `{"@type":"xsd:int","@value":1}`, `{"@type":"xsd:int","@value":1}`},
		{"no value", withNS, "replace", cp + "/host", `null`, `null`},
		{"delete", withNS, "delete", cp + "/baseContext", `["a"]`, `[` + str("a") + `]`},
		{
			"connector configuration whole", withNS, "add", "connectorConfiguration",
			`{"configurationProperties":{"host":"h"},"connectorPoolConfiguration":{"maxObjects":5}}`,
			`{"configurationProperties":{"@ns":"urn:c","host":` + str("h") + `},"connectorPoolConfiguration":{"maxObjects":5}}`,
		},
		{
			"configuration properties whole without a configured namespace", withoutNS, "add", cp,
			`{"host":"h"}`, `{"host":` + str("h") + `}`,
		},
		{
			"configuration properties whole as a list", withNS, "replace", cp,
			`[{"host":"h","bindPassword":{"externalData":{"provider":"env","key":"K"}}}]`,
			`[{"@ns":"urn:c","host":` + str("h") + `,"bindPassword":` + secret + `}]`,
		},
		{"other item", withNS, "replace", "description", `"d"`, `"d"`},
		{"schema handling", withNS, "replace", "schemaHandling/objectType[1]/displayName", `"x"`, `"x"`},
		{"pool configuration", withNS, "replace", "connectorConfiguration/connectorPoolConfiguration/maxObjects", `5`, `5`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			desired := wireMap(t, tc.desired)
			in := []client.ItemDelta{{ModificationType: tc.mod, Path: tc.path, Value: wireJSON(t, tc.value)}}
			before := []client.ItemDelta{{ModificationType: tc.mod, Path: tc.path, Value: wireJSON(t, tc.value)}}
			got := connectorDeltas(desired, in)
			want := []client.ItemDelta{{ModificationType: tc.mod, Path: tc.path, Value: wireJSON(t, tc.want)}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %s\nwant %s", mustJSON(t, got), mustJSON(t, want))
			}
			if !reflect.DeepEqual(in, before) {
				t.Errorf("input mutated: %s", mustJSON(t, in))
			}
		})
	}
}
