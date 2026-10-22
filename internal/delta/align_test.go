package delta

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// mustJSON decodes s with json.Number numbers, like canonical bodies.
func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decoding %s: %v", s, err)
	}
	return m
}

func TestAlign(t *testing.T) {
	tests := []struct {
		name       string
		desired    string
		actual     string
		wantActual string
	}{
		{
			name:       "filter_drops_actual_oid",
			desired:    `{"ownerRef":{"filter":{"filter":{"text":"name = \"admin\""}},"type":"UserType","relation":"org:default"}}`,
			actual:     `{"ownerRef":{"oid":"u1","filter":{"filter":{"text":"name = \"admin\""}},"type":"UserType","relation":"org:default"}}`,
			wantActual: `{"ownerRef":{"filter":{"filter":{"text":"name = \"admin\""}},"type":"UserType","relation":"org:default"}}`,
		},
		{
			name:       "oid_drops_actual_filter",
			desired:    `{"connectorRef":{"oid":"c1","type":"ConnectorType","relation":"org:default"}}`,
			actual:     `{"connectorRef":{"oid":"c1","filter":{"filter":{"text":"connectorType = \"x\""}},"type":"ConnectorType","relation":"org:default"}}`,
			wantActual: `{"connectorRef":{"oid":"c1","type":"ConnectorType","relation":"org:default"}}`,
		},
		{
			name:       "oid_and_filter_keeps_both",
			desired:    `{"connectorRef":{"oid":"c1","filter":{"filter":{"text":"a"}},"relation":"org:default","type":"ConnectorType"}}`,
			actual:     `{"connectorRef":{"oid":"c1","filter":{"filter":{"text":"b"}},"relation":"org:default","type":"ConnectorType"}}`,
			wantActual: `{"connectorRef":{"oid":"c1","filter":{"filter":{"text":"b"}},"relation":"org:default","type":"ConnectorType"}}`,
		},
		{
			name:       "missing_type_dropped",
			desired:    `{"targetRef":{"oid":"r1","relation":"org:default"}}`,
			actual:     `{"targetRef":{"oid":"r1","type":"RoleType","relation":"org:default"}}`,
			wantActual: `{"targetRef":{"oid":"r1","relation":"org:default"}}`,
		},
		{
			name:       "missing_type_annotation_dropped",
			desired:    `{"resourceRef":{"oid":"res1","type":"ResourceType","relation":"org:default"}}`,
			actual:     `{"resourceRef":{"@type":"ObjectReferenceType","oid":"res1","type":"ResourceType","relation":"org:default"}}`,
			wantActual: `{"resourceRef":{"oid":"res1","type":"ResourceType","relation":"org:default"}}`,
		},
		{
			name:       "configured_type_annotation_kept",
			desired:    `{"resourceRef":{"@type":"ObjectReferenceType","oid":"res1"}}`,
			actual:     `{"resourceRef":{"@type":"ObjectReferenceType","oid":"res1","relation":"org:default"}}`,
			wantActual: `{"resourceRef":{"@type":"ObjectReferenceType","oid":"res1"}}`,
		},
		{
			name:       "missing_relation_dropped_when_default",
			desired:    `{"targetRef":{"oid":"r1","type":"RoleType"}}`,
			actual:     `{"targetRef":{"oid":"r1","type":"RoleType","relation":"org:default"}}`,
			wantActual: `{"targetRef":{"oid":"r1","type":"RoleType"}}`,
		},
		{
			name:       "missing_relation_kept_when_not_default",
			desired:    `{"targetRef":{"oid":"r1","type":"RoleType"}}`,
			actual:     `{"targetRef":{"oid":"r1","type":"RoleType","relation":"org:manager"}}`,
			wantActual: `{"targetRef":{"oid":"r1","type":"RoleType","relation":"org:manager"}}`,
		},
		{
			name: "nested_in_inducement_list",
			desired: `{"inducement":[
				{"targetRef":{"filter":{"filter":{"text":"name = \"b\""}},"type":"RoleType","relation":"org:default"}},
				{"targetRef":{"oid":"a1","type":"RoleType","relation":"org:default"}}
			]}`,
			actual: `{"inducement":[
				{"@id":1,"targetRef":{"oid":"a1","type":"RoleType","relation":"org:default"}},
				{"@id":2,"targetRef":{"oid":"b1","filter":{"filter":{"text":"name = \"b\""}},"type":"RoleType","relation":"org:default"}},
				{"@id":3,"targetRef":{"oid":"c1","type":"RoleType","relation":"org:default"}}
			]}`,
			wantActual: `{"inducement":[
				{"@id":2,"targetRef":{"filter":{"filter":{"text":"name = \"b\""}},"type":"RoleType","relation":"org:default"}},
				{"@id":1,"targetRef":{"oid":"a1","type":"RoleType","relation":"org:default"}},
				{"@id":3,"targetRef":{"oid":"c1","type":"RoleType","relation":"org:default"}}
			]}`,
		},
		{
			name:       "nested_in_construction",
			desired:    `{"inducement":[{"construction":{"resourceRef":{"oid":"res1","relation":"org:default"},"kind":"account"}}]}`,
			actual:     `{"inducement":[{"@id":4,"construction":{"resourceRef":{"oid":"res1","type":"ResourceType","relation":"org:default"},"kind":"account"}}]}`,
			wantActual: `{"inducement":[{"@id":4,"construction":{"resourceRef":{"oid":"res1","relation":"org:default"},"kind":"account"}}]}`,
		},
		{
			name:       "scalar_list_reordered_to_desired",
			desired:    `{"subtype":["a","b","c"]}`,
			actual:     `{"subtype":["c","x","a"]}`,
			wantActual: `{"subtype":["a","c","x"]}`,
		},
		{
			name:       "fallback_container_matched_as_set",
			desired:    `{"authorization":[{"decision":"allow","action":["read","add"]}]}`,
			actual:     `{"authorization":[{"@id":1,"decision":"allow","action":["add","read"]}]}`,
			wantActual: `{"authorization":[{"@id":1,"decision":"allow","action":["read","add"]}]}`,
		},
		{
			name:       "compare_only_when_configured_dropped",
			desired:    `{"name":"t"}`,
			actual:     `{"name":"t","oid":"o1","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
			wantActual: `{"name":"t"}`,
		},
		{
			name:       "compare_only_when_configured_kept",
			desired:    `{"name":"t","oid":"o1","binding":"tight","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"}}`,
			actual:     `{"name":"t","oid":"o1","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
			wantActual: `{"name":"t","oid":"o1","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
		},
		{
			name:       "compare_only_when_configured_root_only",
			desired:    `{"activity":{"work":{}}}`,
			actual:     `{"activity":{"work":{},"binding":"x"}}`,
			wantActual: `{"activity":{"work":{},"binding":"x"}}`,
		},
		{
			name:       "unmatched_reference_untouched",
			desired:    `{"assignment":[{"targetRef":{"oid":"r1"}}]}`,
			actual:     `{"assignment":[{"@id":1,"targetRef":{"oid":"r2","type":"RoleType","relation":"org:default"}}]}`,
			wantActual: `{"assignment":[{"@id":1,"targetRef":{"oid":"r2","type":"RoleType","relation":"org:default"}}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired, actual := mustJSON(t, tt.desired), mustJSON(t, tt.actual)
			pristineD, pristineA := mustJSON(t, tt.desired), mustJSON(t, tt.actual)

			d, a := Align(desired, actual)

			if !reflect.DeepEqual(desired, pristineD) || !reflect.DeepEqual(actual, pristineA) {
				t.Fatal("Align mutated its inputs")
			}
			if !reflect.DeepEqual(d, pristineD) {
				t.Errorf("aligned desired = %v, want it unchanged %v", d, pristineD)
			}
			if want := mustJSON(t, tt.wantActual); !reflect.DeepEqual(a, want) {
				t.Errorf("aligned actual =\n%s\nwant\n%s", dump(t, a), dump(t, want))
			}
			// Align is idempotent.
			d2, a2 := Align(d, a)
			if !reflect.DeepEqual(d2, d) || !reflect.DeepEqual(a2, a) {
				t.Errorf("Align is not idempotent: second pass gave %s", dump(t, a2))
			}
		})
	}
}

func TestAlign_copies(t *testing.T) {
	desired := mustJSON(t, `{"assignment":[{"targetRef":{"oid":"r1"}}]}`)
	actual := mustJSON(t, `{"assignment":[{"@id":1,"targetRef":{"oid":"r1"}}]}`)
	d, a := Align(desired, actual)
	d["assignment"].([]any)[0].(map[string]any)["targetRef"].(map[string]any)["oid"] = "x"
	a["assignment"].([]any)[0].(map[string]any)["targetRef"].(map[string]any)["oid"] = "y"
	if desired["assignment"].([]any)[0].(map[string]any)["targetRef"].(map[string]any)["oid"] != "r1" ||
		actual["assignment"].([]any)[0].(map[string]any)["targetRef"].(map[string]any)["oid"] != "r1" {
		t.Error("Align results share structure with its inputs")
	}
}

func dump(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
