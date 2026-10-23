package normalize

import (
	"encoding/json"
	"testing"
)

func TestFilterText(t *testing.T) {
	eq := func(path string, v any) map[string]any {
		return map[string]any{"path": path, "value": []any{v}}
	}
	tests := []struct {
		name   string
		filter map[string]any
		want   string
		ok     bool
	}{
		{"text", map[string]any{"text": `name = "x"`}, `name = "x"`, true},
		{"equal", map[string]any{"equal": eq("name", "vault / operator")}, `name = "vault / operator"`, true},
		{"quote and backslash", map[string]any{"equal": eq("name", `a "b" \c`)}, `name = "a \"b\" \\c"`, true},
		{"bool", map[string]any{"equal": eq("x", true)}, `x = true`, true},
		{"json number", map[string]any{"equal": eq("x", json.Number("7"))}, `x = 7`, true},
		{"and", map[string]any{"and": map[string]any{"equal": []any{eq("name", "a"), eq("order", 2.0)}}}, `name = "a" and order = 2`, true},
		{"or in and", map[string]any{"and": map[string]any{"equal": eq("a", "1"), "or": map[string]any{"equal": []any{eq("b", "2"), eq("c", true)}}}},
			`a = "1" and (b = "2" or c = true)`, true},
		{"top or", map[string]any{"or": map[string]any{"equal": []any{eq("b", "2"), eq("c", "3")}}}, `b = "2" or c = "3"`, true},
		{"not", map[string]any{"not": map[string]any{"equal": eq("name", "a")}}, `not (name = "a")`, true},
		{"substring", map[string]any{"substring": eq("name", "a")}, "", false},
		{"matching", map[string]any{"equal": map[string]any{"path": "name", "value": []any{"a"}, "matching": "polyStringNorm"}}, "", false},
		{"two values", map[string]any{"equal": map[string]any{"path": "name", "value": []any{"a", "b"}}}, "", false},
		{"empty", map[string]any{}, "", false},
		{"nil", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FilterText(tt.filter)
			if ok != tt.ok || got != tt.want {
				t.Errorf("FilterText = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
