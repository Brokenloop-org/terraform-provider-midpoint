package provider

import "testing"

func TestRefFromJSON_structuredFilter(t *testing.T) {
	structured := map[string]any{"equal": map[string]any{"path": "name", "value": []any{"vault / operator"}}}
	tests := []struct {
		name   string
		filter any
		want   string
		ok     bool
	}{
		// The text midpoint-convert writes for this filter.
		{"equal", structured, `name = "vault / operator"`, true},
		{"text", map[string]any{"text": `name = "x"`}, `name = "x"`, true},
		{"unsupported", map[string]any{"substring": map[string]any{"path": "name", "value": []any{"v"}}}, "", false},
		{"not a map", "name = x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := map[string]any{"type": "RoleType", "relation": defaultRelation, "filter": tt.filter}
			got, ok := refFromJSON(ref)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got.Filter.ValueString() != tt.want {
				t.Errorf("filter = %q, want %q", got.Filter.ValueString(), tt.want)
			}
		})
	}
}

func TestAssignmentFromJSON_structuredFilter(t *testing.T) {
	v := map[string]any{"targetRef": map[string]any{
		"type": "RoleType", "relation": defaultRelation,
		"filter": map[string]any{"equal": map[string]any{"path": "name", "value": []any{"vault / operator"}}},
	}}
	a, ok := assignmentFromJSON("roles", "inducement", v, nil)
	if !ok || a.Target == nil || a.Target.Filter.ValueString() != `name = "vault / operator"` {
		t.Fatalf("block = %+v, ok = %v", a.Target, ok)
	}
}
