package normalize

import (
	"encoding/json"
	"testing"
)

func TestEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		// numbers
		{"numbers json.Number vs float64", json.Number("2"), float64(2), true},
		{"numbers json.Number vs int64", json.Number("2"), int64(2), true},
		{"numbers float64 vs int64", float64(2), int64(2), true},
		{"numbers int vs json.Number", 2, json.Number("2.0"), true},
		{"numbers differ", json.Number("2"), float64(3), false},
		{"numbers large exact", json.Number("1000000000000000070"), json.Number("1000000000000000071"), false},
		{"numbers string vs number", "2", json.Number("2"), false},
		{"numbers number vs string", float64(2), "2", false},
		{"numbers nested in maps", map[string]any{"n": json.Number("1")}, map[string]any{"n": float64(1)}, true},

		// ignores_at_id
		{"ignores_at_id one side", map[string]any{"@id": json.Number("3"), "a": "x"}, map[string]any{"a": "x"}, true},
		{"ignores_at_id different ids", map[string]any{"@id": 1, "a": "x"}, map[string]any{"@id": 2, "a": "x"}, true},
		{"ignores_at_id nested", []any{map[string]any{"@id": 1, "t": map[string]any{"@id": 9, "v": "y"}}}, []any{map[string]any{"t": map[string]any{"v": "y"}}}, true},
		{"ignores_at_id but not other keys", map[string]any{"@id": 1, "a": "x"}, map[string]any{"@id": 1, "a": "y"}, false},

		// order_sensitive_lists
		{"order_sensitive_lists same order", []any{"a", "b"}, []any{"a", "b"}, true},
		{"order_sensitive_lists swapped", []any{"a", "b"}, []any{"b", "a"}, false},
		{"order_sensitive_lists length", []any{"a"}, []any{"a", "a"}, false},
		{"order_sensitive_lists list vs scalar", []any{"a"}, "a", false},

		// nil_vs_missing
		{"nil_vs_missing key", map[string]any{"a": "x", "b": nil}, map[string]any{"a": "x"}, true},
		{"nil_vs_missing reversed", map[string]any{"a": "x"}, map[string]any{"a": "x", "b": nil}, true},
		{"nil_vs_missing non-nil differs", map[string]any{"a": "x", "b": "y"}, map[string]any{"a": "x"}, false},
		{"nil vs nil", nil, nil, true},
		{"nil vs empty string", nil, "", false},

		// scalars and shapes
		{"bool equal", true, true, true},
		{"bool differ", true, false, false},
		{"string equal", "a", "a", true},
		{"map vs list", map[string]any{}, []any{}, false},
		{"empty maps", map[string]any{}, map[string]any{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Equal(tc.a, tc.b); got != tc.want {
				t.Errorf("Equal(%#v, %#v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := Equal(tc.b, tc.a); got != tc.want {
				t.Errorf("Equal is not symmetric for %#v, %#v", tc.a, tc.b)
			}
		})
	}
}

func TestNumberEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"json.Number vs float64", json.Number("2"), float64(2), true},
		{"json.Number vs int64", json.Number("2"), int64(2), true},
		{"float64 vs int", float64(2.5), 2, false},
		{"fractional", json.Number("2.5"), float64(2.5), true},
		{"exponent", json.Number("1e3"), int64(1000), true},
		{"uint", uint32(7), json.Number("7"), true},
		{"string is not a number", "2", float64(2), false},
		{"nil is not a number", nil, float64(0), false},
		{"invalid json.Number", json.Number("x"), json.Number("x"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NumberEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("NumberEqual(%#v, %#v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
