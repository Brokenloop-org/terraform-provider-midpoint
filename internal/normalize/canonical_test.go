package normalize

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden testdata/*.out.json files")

// goldenInput is the shape of testdata/<case>.in.json.
type goldenInput struct {
	RestType string         `json:"restType"`
	Body     map[string]any `json:"body"`
}

func decodeJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decoding: %v", err)
	}
}

// requiredCases are the golden cases the brief mandates; the glob below must
// find each of them so that a deleted fixture fails loudly.
var requiredCases = []string{
	"strips_metadata_and_version",
	"single_value_container_becomes_list",
	"strips_key_prefixes",
	"reference_type_prefix",
	"reference_default_relation",
	"drops_targetName",
	"folds_polystring_name",
	"task_strips_execution_state",
	"resource_strips_native_capabilities",
	"keeps_container_ids",
	"real_superuser_role",
	"real_task",
	"qname_values_normalized",
	"real_resource_csv",
	"real_resource_csv_synchronization",
}

func TestCanonical(t *testing.T) {
	ins, err := filepath.Glob(filepath.Join("testdata", "*.in.json"))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, in := range ins {
		found[strings.TrimSuffix(filepath.Base(in), ".in.json")] = true
	}
	for _, c := range requiredCases {
		if !found[c] {
			t.Errorf("missing golden case testdata/%s.in.json", c)
		}
	}

	for _, in := range ins {
		name := strings.TrimSuffix(filepath.Base(in), ".in.json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			var input, pristine goldenInput
			decodeJSON(t, raw, &input)
			decodeJSON(t, raw, &pristine)
			if input.RestType == "" || input.Body == nil {
				t.Fatalf("%s: restType and body are required", in)
			}

			got := Canonical(input.RestType, input.Body)

			if !reflect.DeepEqual(input.Body, pristine.Body) {
				t.Fatalf("Canonical mutated its input")
			}
			gotJSON, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			gotJSON = append(gotJSON, '\n')

			outPath := filepath.Join("testdata", name+".out.json")
			if *update {
				if err := os.WriteFile(outPath, gotJSON, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("reading golden output (run with -update to create): %v", err)
			}
			if !bytes.Equal(gotJSON, want) {
				t.Errorf("Canonical(%q) mismatch\n--- got ---\n%s\n--- want ---\n%s", input.RestType, gotJSON, want)
			}
		})
	}
}

func TestCanonical_idempotent(t *testing.T) {
	ins, err := filepath.Glob(filepath.Join("testdata", "*.in.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range ins {
		name := strings.TrimSuffix(filepath.Base(in), ".in.json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			var input goldenInput
			decodeJSON(t, raw, &input)
			once := Canonical(input.RestType, input.Body)
			twice := Canonical(input.RestType, once)
			if !reflect.DeepEqual(once, twice) {
				t.Errorf("Canonical is not idempotent:\nonce:  %v\ntwice: %v", once, twice)
			}
		})
	}
}

func TestCanonical_nil(t *testing.T) {
	if got := Canonical("roles", nil); got != nil {
		t.Errorf("Canonical(nil) = %v, want nil", got)
	}
}

func TestCanonical_rootIsNeverPruned(t *testing.T) {
	got := Canonical("roles", map[string]any{"version": "3", "metadata": map[string]any{"x": "y"}})
	if got == nil || len(got) != 0 {
		t.Errorf("Canonical of an all-server-managed body = %#v, want an empty non-nil map", got)
	}
}

func TestIsServerManaged(t *testing.T) {
	cases := []struct {
		name     string
		restType string
		path     []string
		want     bool
	}{
		{"root metadata", "roles", []string{"metadata"}, true},
		{"nested metadata any depth", "roles", []string{"assignment", "metadata"}, true},
		{"value metadata any depth", "roles", []string{"assignment", "@metadata"}, true},
		{"ns any depth", "roles", []string{"assignment", "@ns"}, true},
		{"root @type", "roles", []string{"@type"}, true},
		{"nested @type is kept", "roles", []string{"assignment", "targetRef", "@type"}, false},
		{"version root only", "roles", []string{"version"}, true},
		{"version nested is configuration", "resources", []string{"connectorConfiguration", "version"}, false},
		{"name is configuration", "roles", []string{"name"}, false},
		{"assignment is configuration", "roles", []string{"assignment"}, false},
		{"root activation effectiveStatus", "roles", []string{"activation", "effectiveStatus"}, true},
		{"assignment activation effectiveStatus", "roles", []string{"assignment", "activation", "effectiveStatus"}, true},
		{"activation administrativeStatus is configuration", "roles", []string{"activation", "administrativeStatus"}, false},
		{"activation container itself is configuration", "roles", []string{"activation"}, false},
		{"credentials wildcard", "users", []string{"credentials", "password", "metadata"}, true},
		{"credentials value is configuration", "users", []string{"credentials", "password", "value"}, false},
		{"targetName any depth", "roles", []string{"inducement", "targetRef", "targetName"}, true},
		{"task-only rule on tasks", "tasks", []string{"executionState"}, true},
		{"task-only rule elsewhere", "roles", []string{"executionState"}, false},
		{"task schedulingStateBeforeSuspend", "tasks", []string{"schedulingStateBeforeSuspend"}, true},
		{"task ownerRef is configuration", "tasks", []string{"ownerRef"}, false},
		{"task activity is configuration", "tasks", []string{"activity"}, false},
		{"resource native capabilities", "resources", []string{"capabilities", "native"}, true},
		{"resource configured capabilities", "resources", []string{"capabilities", "configured"}, false},
		{"resource-only rule elsewhere", "roles", []string{"operationalState"}, false},
		{"resource schema cachingMetadata", "resources", []string{"schema", "cachingMetadata"}, true},
		{"resource schema definition", "resources", []string{"schema", "definition"}, false},
		{"empty path", "roles", nil, false},
		{"focus behavior is server-managed", "users", []string{"behavior"}, true},
		{"report behavior is configuration", "reports", []string{"behavior"}, false},
		{"resource object type behavior is configuration", "resources", []string{"schemaHandling", "objectType", "behavior"}, false},
		{"saml2 idp metadata is configuration", "securityPolicies", []string{"authentication", "modules", "saml2", "serviceProvider", "identityProvider", "metadata"}, false},
		{"function parameter @type", "functionLibraries", []string{"function", "parameter", "@type"}, true},
		{"function parameter is configuration", "functionLibraries", []string{"function", "parameter", "type"}, false},
		{"saml2 idp metadata children are configuration", "securityPolicies", []string{"authentication", "modules", "saml2", "serviceProvider", "identityProvider", "metadata", "metadataUrl"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsServerManaged(tc.restType, tc.path); got != tc.want {
				t.Errorf("IsServerManaged(%q, %v) = %v, want %v", tc.restType, tc.path, got, tc.want)
			}
		})
	}
}

func TestServerManaged_everyRuleHasReason(t *testing.T) {
	for _, r := range append(slices.Clone(ServerManaged), configurationOverrides...) {
		if strings.TrimSpace(r.Reason) == "" {
			t.Errorf("rule %q has no Reason", r.Path)
		}
		if r.Path == "" || strings.HasSuffix(r.Path, "/") || strings.Contains(r.Path, "//") {
			t.Errorf("rule %q has a malformed path", r.Path)
		}
	}
}

// A resource schema definition keeps its "@ns": midPoint parses the XML
// schema only with it, and the delta that replaces a definition carries the
// canonical value. Elsewhere "@ns" is still dropped.
func TestCanonical_resourceSchemaDefinitionKeepsNamespace(t *testing.T) {
	const xsd = "http://www.w3.org/2001/XMLSchema"
	body := map[string]any{
		"@ns": "http://midpoint.evolveum.com/xml/ns/public/common/common-3",
		"schema": map[string]any{
			"definition": map[string]any{"@ns": xsd, "schema": "<xsd:schema/>"},
		},
		"connectorConfiguration": map[string]any{
			"@ns":                     "http://midpoint.evolveum.com/xml/ns/public/connector/icf-1/connector-schema-3",
			"configurationProperties": map[string]any{"@ns": "urn:csv", "filePath": "/f.csv"},
		},
	}
	want := map[string]any{
		"schema": map[string]any{
			"definition": map[string]any{"@ns": xsd, "schema": "<xsd:schema/>"},
		},
		"connectorConfiguration": map[string]any{
			"configurationProperties": map[string]any{"filePath": "/f.csv"},
		},
	}
	if got := Canonical("resources", body); !reflect.DeepEqual(got, want) {
		t.Errorf("Canonical(resources) = %v, want %v", got, want)
	}
	wantRole := map[string]any{"schema": map[string]any{"definition": map[string]any{"schema": "<xsd:schema/>"}}}
	if got := Canonical("roles", map[string]any{"schema": body["schema"]}); !reflect.DeepEqual(got, wantRole) {
		t.Errorf("Canonical(roles) = %v, want %v", got, wantRole)
	}
}

// Correlators and synchronization actions are multi-valued containers that
// midPoint 4.9 returns as a single object when there is one value
// (testdata/real/resource-csv-synchronization.json); a configured list of
// one must compare equal.
func TestCanonical_resourceCorrelatorsAndActionsAreLists(t *testing.T) {
	one := map[string]any{"name": "x"}
	body := map[string]any{"schemaHandling": map[string]any{"objectType": map[string]any{
		"correlation":     map[string]any{"correlators": map[string]any{"items": one}},
		"synchronization": map[string]any{"reaction": map[string]any{"actions": map[string]any{"synchronize": one, "addFocus": one}}},
	}}}
	list := []any{map[string]any{"name": "x"}}
	want := map[string]any{"schemaHandling": map[string]any{"objectType": []any{map[string]any{
		"correlation":     map[string]any{"correlators": map[string]any{"items": list}},
		"synchronization": map[string]any{"reaction": []any{map[string]any{"actions": map[string]any{"synchronize": list, "addFocus": list}}}},
	}}}}
	if got := Canonical("resources", body); !reflect.DeepEqual(got, want) {
		t.Errorf("Canonical = %v, want %v", got, want)
	}
}
