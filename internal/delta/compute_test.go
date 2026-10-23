package delta

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/normalize"
)

var update = flag.Bool("update", false, "rewrite the golden testdata/compute/*/expected.json files")

// requiredComputeCases are the golden cases the brief mandates; a deleted
// fixture fails loudly.
var requiredComputeCases = []string{
	"no_change_empty",
	"scalar_replace",
	"scalar_removed",
	"add_inducement",
	"delete_inducement_by_id",
	"nested_replace_in_container",
	"nested_objecttype_attribute_add",
	"reference_filter_only_needs_reevaluation",
	"extension_scalar_replace",
	"extension_single_value_list_no_change",
}

// needsReevaluation lists the cases whose deltas carry a filter-only
// reference; every other case must not ask for filter reevaluation.
var needsReevaluation = map[string]bool{
	"reference_filter_only_needs_reevaluation": true,
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
}

// asJSON round-trips v through its wire encoding so that deltas compare the
// way midPoint receives them.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

var modificationRank = map[string]int{"delete": 0, "replace": 1, "add": 2}

func TestCompute(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("testdata", "compute", "*"))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, d := range dirs {
		found[filepath.Base(d)] = true
	}
	for _, c := range requiredComputeCases {
		if !found[c] {
			t.Errorf("missing golden case testdata/compute/%s", c)
		}
	}

	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			var desired, actual, pristineD, pristineA map[string]any
			readJSON(t, filepath.Join(dir, "desired.json"), &desired)
			readJSON(t, filepath.Join(dir, "actual.json"), &actual)
			readJSON(t, filepath.Join(dir, "desired.json"), &pristineD)
			readJSON(t, filepath.Join(dir, "actual.json"), &pristineA)

			got, err := Compute(desired, actual)
			if err != nil {
				t.Fatalf("Compute: %v", err)
			}
			if !reflect.DeepEqual(desired, pristineD) || !reflect.DeepEqual(actual, pristineA) {
				t.Fatal("Compute mutated its inputs")
			}

			expectedPath := filepath.Join(dir, "expected.json")
			if *update {
				raw, err := json.MarshalIndent(got, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(expectedPath, append(raw, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var want []any
			readJSON(t, expectedPath, &want)
			if want == nil {
				want = []any{}
			}
			gotJSON := asJSON(t, got)
			if gotJSON == nil {
				gotJSON = []any{}
			}
			if !reflect.DeepEqual(gotJSON, any(want)) {
				t.Errorf("Compute =\n%s\nwant\n%s", dump(t, gotJSON), dump(t, want))
			}

			checkInvariants(t, desired, actual, got)

			if g, w := NeedsFilterReevaluation(got), needsReevaluation[name]; g != w {
				t.Errorf("NeedsFilterReevaluation = %v, want %v", g, w)
			}

			// The engine aligns before computing; aligned input yields the
			// same deltas.
			d, a := Align(desired, actual)
			again, err := Compute(d, a)
			if err != nil {
				t.Fatalf("Compute(Align(...)): %v", err)
			}
			if !reflect.DeepEqual(asJSON(t, again), asJSON(t, got)) {
				t.Errorf("Compute on aligned input =\n%s\nwant\n%s", dump(t, again), dump(t, got))
			}
		})
	}
}

// checkInvariants holds for every delta set: grouped deletes, replaces, adds;
// relative paths; never a whole-object replace; no deltas when the aligned
// bodies are equal, and deltas when they are not.
func checkInvariants(t *testing.T, desired, actual map[string]any, got []client.ItemDelta) {
	t.Helper()
	last := -1
	for _, dl := range got {
		rank, ok := modificationRank[dl.ModificationType]
		if !ok {
			t.Errorf("unknown modificationType %q", dl.ModificationType)
		}
		if rank < last {
			t.Errorf("deltas not ordered delete, replace, add: %+v", got)
		}
		last = rank
		if dl.Path == "" || strings.HasPrefix(dl.Path, "/") || strings.Contains(dl.Path, ":") || strings.Contains(dl.Path, "@") {
			t.Errorf("bad path %q", dl.Path)
		}
	}
	d, a := Align(desired, actual)
	if equal := normalize.Equal(d, a); equal != (len(got) == 0) {
		t.Errorf("Equal(Align(desired, actual)) = %v but Compute returned %d deltas", equal, len(got))
	}
}

func TestCompute_errors(t *testing.T) {
	tests := []struct {
		name            string
		desired, actual string
	}{
		{
			name:    "fractional_container_id",
			desired: `{"inducement":[{"targetRef":{"oid":"a"},"description":"x"}]}`,
			actual:  `{"inducement":[{"@id":1.5,"targetRef":{"oid":"a"}}]}`,
		},
		{
			name:    "root_annotation_differs",
			desired: `{"@type":"RoleType","name":"r"}`,
			actual:  `{"@type":"UserType","name":"r"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Compute(mustJSON(t, tt.desired), mustJSON(t, tt.actual)); err == nil {
				t.Errorf("Compute = %+v, want an error", got)
			}
		})
	}
}

func TestNeedsFilterReevaluation(t *testing.T) {
	filterRef := map[string]any{"filter": map[string]any{"filter": map[string]any{"text": `name = "x"`}}, "type": "RoleType"}
	both := map[string]any{"oid": "r1", "filter": map[string]any{"filter": map[string]any{"text": `name = "x"`}}}
	tests := []struct {
		name   string
		deltas []client.ItemDelta
		want   bool
	}{
		{"none", nil, false},
		{"replace_reference", []client.ItemDelta{{ModificationType: "replace", Path: "ownerRef", Value: filterRef}}, true},
		{"nested_add", []client.ItemDelta{{ModificationType: "add", Path: "inducement", Value: []any{map[string]any{"targetRef": filterRef}}}}, true},
		{"deep_in_construction", []client.ItemDelta{{ModificationType: "add", Path: "assignment[1]/construction",
			Value: map[string]any{"resourceRef": filterRef, "kind": "account"}}}, true},
		{"oid_and_filter", []client.ItemDelta{{ModificationType: "add", Path: "inducement", Value: []any{map[string]any{"targetRef": both}}}}, false},
		{"delete_ignored", []client.ItemDelta{{ModificationType: "delete", Path: "inducement", Value: []any{map[string]any{"targetRef": filterRef}}}}, false},
		{"query_filter_is_not_reference", []client.ItemDelta{{ModificationType: "replace", Path: "activity/work/recomputation/objects/query",
			Value: map[string]any{"filter": map[string]any{"text": "x"}}}}, false},
		{"replace_without_value", []client.ItemDelta{{ModificationType: "replace", Path: "ownerRef"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsFilterReevaluation(tt.deltas); got != tt.want {
				t.Errorf("NeedsFilterReevaluation = %v, want %v", got, tt.want)
			}
		})
	}
}
