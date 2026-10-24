package delta

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/normalize"
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
			name:       "structured_filter_rendering_desired_text_takes_text_form",
			desired:    `{"inducement":[{"targetRef":{"filter":{"text":"name = \"g\""},"type":"RoleType"}}]}`,
			actual:     `{"inducement":[{"@id":1,"targetRef":{"oid":"g1","filter":{"equal":{"path":"name","value":["g"]}},"type":"RoleType","relation":"org:default"}}]}`,
			wantActual: `{"inducement":[{"@id":1,"targetRef":{"filter":{"text":"name = \"g\""},"type":"RoleType"}}]}`,
		},
		{
			name:       "structured_and_filter_in_any_order_takes_text_form",
			desired:    `{"connectorRef":{"filter":{"text":"connectorType = \"t\" and connectorVersion = \"2\""},"type":"ConnectorType"}}`,
			actual:     `{"connectorRef":{"oid":"c1","filter":{"and":{"equal":[{"path":"connectorVersion","value":"2"},{"path":"connectorType","value":"t"}]}},"type":"ConnectorType"}}`,
			wantActual: `{"connectorRef":{"filter":{"text":"connectorType = \"t\" and connectorVersion = \"2\""},"type":"ConnectorType"}}`,
		},
		{
			name:       "structured_filter_rendering_other_text_untouched",
			desired:    `{"targetRef":{"filter":{"text":"name = \"g\""}}}`,
			actual:     `{"targetRef":{"oid":"h1","filter":{"equal":{"path":"name","value":"h"}}}}`,
			wantActual: `{"targetRef":{"filter":{"equal":{"path":"name","value":"h"}}}}`,
		},
		{
			name:       "unrenderable_structured_filter_untouched",
			desired:    `{"targetRef":{"filter":{"text":"name = \"g\""}}}`,
			actual:     `{"targetRef":{"filter":{"substring":{"path":"name","value":"g"}}}}`,
			wantActual: `{"targetRef":{"filter":{"substring":{"path":"name","value":"g"}}}}`,
		},
		{
			name:       "other_text_filter_untouched",
			desired:    `{"targetRef":{"filter":{"text":"name = \"g\""}}}`,
			actual:     `{"targetRef":{"filter":{"text":"name = 'g'"}}}`,
			wantActual: `{"targetRef":{"filter":{"text":"name = 'g'"}}}`,
		},
		{
			name:       "desired_structured_filter_untouched",
			desired:    `{"targetRef":{"filter":{"equal":{"path":"name","value":"h"}}}}`,
			actual:     `{"targetRef":{"filter":{"equal":{"path":"name","value":"g"}}}}`,
			wantActual: `{"targetRef":{"filter":{"equal":{"path":"name","value":"g"}}}}`,
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
			// Only oid is compared only when configured for every type;
			// ownerRef and binding are so for tasks (TestAlignForType).
			name:       "compare_only_when_configured_dropped",
			desired:    `{"name":"t"}`,
			actual:     `{"name":"t","oid":"o1","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
			wantActual: `{"name":"t","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
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
			// midPoint returns a single value of an extension item as that
			// value; the configured one-element list is the same value.
			name:       "single_value_list_matches_scalar",
			desired:    `{"extension":{"costCenter":["a"],"level":[3]}}`,
			actual:     `{"extension":{"costCenter":"a","level":3.0}}`,
			wantActual: `{"extension":{"costCenter":["a"],"level":[3.0]}}`,
		},
		{
			name:       "scalar_matches_single_value_list",
			desired:    `{"extension":{"costCenter":"a"}}`,
			actual:     `{"extension":{"costCenter":["a"]}}`,
			wantActual: `{"extension":{"costCenter":"a"}}`,
		},
		{
			name:       "single_value_list_other_value_untouched",
			desired:    `{"extension":{"costCenter":["a"],"tags":["x","y"],"obj":[{"k":"a"}]}}`,
			actual:     `{"extension":{"costCenter":"b","tags":"x","obj":{"k":"a"}}}`,
			wantActual: `{"extension":{"costCenter":"b","tags":"x","obj":{"k":"a"}}}`,
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

func TestAlignForType(t *testing.T) {
	// The schema midPoint fetches from the connector and caches in the
	// resource (recorded in normalize/testdata/real/resource-csv.json).
	const cachedDefinition = `{"@ns":"http://www.w3.org/2001/XMLSchema","schema":"<xsd:schema/>"}`
	archetype := `{"@id":4,"targetRef":{"oid":"00000000-0000-0000-0000-000000000502","type":"ArchetypeType","relation":"org:default"}}`
	tests := []struct {
		name, restType, desired, actual, wantActual string
	}{
		{
			name:       "task_unconfigured_assignment_dropped",
			restType:   "tasks",
			desired:    `{"name":"t"}`,
			actual:     `{"name":"t","assignment":[` + archetype + `],"ownerRef":{"oid":"u1"}}`,
			wantActual: `{"name":"t"}`,
		},
		{
			name:       "task_unconfigured_owner_and_binding_dropped",
			restType:   "tasks",
			desired:    `{"name":"t"}`,
			actual:     `{"name":"t","oid":"o1","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
			wantActual: `{"name":"t"}`,
		},
		{
			name:       "task_configured_owner_and_binding_compared",
			restType:   "tasks",
			desired:    `{"name":"t","ownerRef":{"oid":"u2","type":"UserType","relation":"org:default"},"binding":"tight"}`,
			actual:     `{"name":"t","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
			wantActual: `{"name":"t","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
		},
		{
			// ownerRef and binding of other types are configuration.
			name:       "object_owner_and_binding_kept",
			restType:   "objectCollections",
			desired:    `{"name":"c"}`,
			actual:     `{"name":"c","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
			wantActual: `{"name":"c","ownerRef":{"oid":"u1","type":"UserType","relation":"org:default"},"binding":"loose"}`,
		},
		{
			name:       "task_configured_assignment_compared_whole",
			restType:   "tasks",
			desired:    `{"name":"t","assignment":[{"targetRef":{"oid":"r1","type":"RoleType","relation":"org:default"}}]}`,
			actual:     `{"name":"t","assignment":[` + archetype + `]}`,
			wantActual: `{"name":"t","assignment":[` + archetype + `]}`,
		},
		{
			name:       "resource_unconfigured_schema_definition_dropped",
			restType:   "resources",
			desired:    `{"name":"r"}`,
			actual:     `{"name":"r","schema":{"definition":` + cachedDefinition + `}}`,
			wantActual: `{"name":"r"}`,
		},
		{
			name:       "resource_unconfigured_schema_definition_dropped_generation_constraints_kept",
			restType:   "resources",
			desired:    `{"name":"r","schema":{"generationConstraints":{"generateObjectClass":["ri:A"]}}}`,
			actual:     `{"name":"r","schema":{"generationConstraints":{"generateObjectClass":["ri:A"]},"definition":` + cachedDefinition + `}}`,
			wantActual: `{"name":"r","schema":{"generationConstraints":{"generateObjectClass":["ri:A"]}}}`,
		},
		{
			name:       "resource_configured_schema_definition_compared",
			restType:   "resources",
			desired:    `{"name":"r","schema":{"definition":{"schema":"<mine/>"}}}`,
			actual:     `{"name":"r","schema":{"definition":` + cachedDefinition + `}}`,
			wantActual: `{"name":"r","schema":{"definition":` + cachedDefinition + `}}`,
		},
		{
			name:       "task_schema_definition_kept",
			restType:   "tasks",
			desired:    `{"name":"t"}`,
			actual:     `{"name":"t","schema":{"definition":` + cachedDefinition + `}}`,
			wantActual: `{"name":"t","schema":{"definition":` + cachedDefinition + `}}`,
		},
		{
			name:       "role_unconfigured_assignment_kept",
			restType:   "roles",
			desired:    `{"name":"r"}`,
			actual:     `{"name":"r","assignment":[` + archetype + `]}`,
			wantActual: `{"name":"r","assignment":[` + archetype + `]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired, actual := mustJSON(t, tt.desired), mustJSON(t, tt.actual)
			pristineA := mustJSON(t, tt.actual)
			d, a := AlignForType(tt.restType, desired, actual)
			if !reflect.DeepEqual(actual, pristineA) {
				t.Fatal("AlignForType mutated its input")
			}
			if want := mustJSON(t, tt.wantActual); !reflect.DeepEqual(a, want) {
				t.Errorf("aligned actual =\n%s\nwant\n%s", dump(t, a), dump(t, want))
			}
			if d2, a2 := AlignForType(tt.restType, d, a); !reflect.DeepEqual(d2, d) || !reflect.DeepEqual(a2, a) {
				t.Errorf("AlignForType is not idempotent: second pass gave %s", dump(t, a2))
			}
		})
	}
}

// capabilities and schema of a resource hold server-managed parts (native
// capabilities, the cached schema) that Canonical strips or that are
// compared only when configured: configuring or removing the configured
// part is a delta inside the container, never one of the whole container.
func TestAlignForType_resourcePartialContainersNarrowDeltas(t *testing.T) {
	const cached = `{"@ns":"http://www.w3.org/2001/XMLSchema","schema":"<xsd:schema/>"}`
	const caps = `{"create":{"enabled":false}}`
	const gc = `{"generateObjectClass":["ri:A"]}`
	tests := []struct {
		name, desired, actual, want string
	}{
		{
			// Canonical pruned capabilities: only native was there.
			name:    "capabilities_configured_added",
			desired: `{"name":"r","capabilities":{"configured":` + caps + `}}`,
			actual:  `{"name":"r"}`,
			want:    `[{"modificationType":"add","path":"capabilities/configured","value":` + caps + `}]`,
		},
		{
			name:    "capabilities_configured_removed",
			desired: `{"name":"r"}`,
			actual:  `{"name":"r","capabilities":{"configured":` + caps + `}}`,
			want:    `[{"modificationType":"replace","path":"capabilities/configured"}]`,
		},
		{
			name:    "generation_constraints_added_next_to_cached_schema",
			desired: `{"name":"r","schema":{"generationConstraints":` + gc + `}}`,
			actual:  `{"name":"r","schema":{"definition":` + cached + `}}`,
			want:    `[{"modificationType":"add","path":"schema/generationConstraints","value":` + gc + `}]`,
		},
		{
			name:    "generation_constraints_removed_next_to_cached_schema",
			desired: `{"name":"r"}`,
			actual:  `{"name":"r","schema":{"generationConstraints":` + gc + `,"definition":` + cached + `}}`,
			want:    `[{"modificationType":"replace","path":"schema/generationConstraints"}]`,
		},
		{
			name:    "generation_constraints_added_without_schema",
			desired: `{"name":"r","schema":{"generationConstraints":` + gc + `}}`,
			actual:  `{"name":"r"}`,
			want:    `[{"modificationType":"add","path":"schema/generationConstraints","value":` + gc + `}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, a := AlignForType("resources", mustJSON(t, tt.desired), mustJSON(t, tt.actual))
			if d2, a2 := AlignForType("resources", d, a); !reflect.DeepEqual(d2, d) || !reflect.DeepEqual(a2, a) {
				t.Errorf("AlignForType is not idempotent: second pass gave %s / %s", dump(t, d2), dump(t, a2))
			}
			if normalize.Equal(d, a) {
				t.Fatalf("aligned bodies compare equal: %s", dump(t, a))
			}
			got, err := Compute(d, a)
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatal(err)
			}
			if g := asJSON(t, got); !reflect.DeepEqual(g, want) {
				t.Errorf("deltas =\n%s\nwant\n%s", dump(t, g), dump(t, want))
			}
		})
	}
}

func TestPair(t *testing.T) {
	body := mustJSON(t, `{
		"d": [
			{"targetRef":{"filter":{"text":"name = \"b\""},"type":"RoleType","relation":"org:default"}},
			{"targetRef":{"oid":"a1","type":"RoleType","relation":"org:default"}},
			{"targetRef":{"oid":"z9","type":"RoleType","relation":"org:default"}}
		],
		"a": [
			{"@id":1,"targetRef":{"oid":"a1","type":"RoleType","relation":"org:default"}},
			{"@id":2,"targetRef":{"oid":"b1","filter":{"text":"name = \"b\""},"type":"RoleType","relation":"org:default"}}
		]
	}`)
	got := Pair("inducement", body["d"].([]any), body["a"].([]any))
	if want := []int{1, 0, -1}; !reflect.DeepEqual(got, want) {
		t.Errorf("Pair = %v, want %v", got, want)
	}
}
