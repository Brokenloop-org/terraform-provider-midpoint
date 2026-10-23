package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
	"github.com/jplana/terraform-provider-midpoint/internal/convert"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// accConvertRoleOID is the fixture role whose inducement midPoint stores
// with a structured filter; accConvertResourceOID the fixture resource whose
// connectorRef is a structured filter; accConvertBuiltinOID the built-in
// archetype the catalogue customizes.
const (
	accConvertRoleOID     = "e7b30a96-4d85-4c21-b1f7-92a05d6c8e44"
	accConvertResourceOID = "25e166d6-9899-43b7-b0d7-4c4c526b7a9e"
	accConvertBuiltinOID  = "00000000-0000-0000-0000-000000000328"
)

// TestAccConvert_roundTrip runs the migration guide against midPoint:
// it seeds the fixture catalogue as it is (raw JSON, structured filters on
// the business roles and in the resource's connectorRef), exports and
// converts what midPoint holds, plans the converted configuration (imports
// only, no change), applies the imports without any write, removes them and
// plans again (empty), then edits one description on a role with a
// structured filter inducement and checks that midPoint changed that
// description and nothing else: the inducement keeps its @id and its stored
// filter, the connectorRef and the built-in archetype stay untouched.
func TestAccConvert_roundTrip(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	ctx := context.Background()

	// The fixture objects: the only ones this test creates, and never a
	// built-in. A built-in in the catalogue already exists; it is snapshot
	// here and restored at the end.
	catalogue := filepath.Join("..", "convert", "testdata", "catalogue")
	srcs, _, err := convert.Load([]string{catalogue})
	if err != nil {
		t.Fatal(err)
	}
	type fixtureObject struct{ restType, oid, path string }
	var fixtures []fixtureObject
	for _, s := range srcs {
		if !client.IsBuiltinOID(s.OID) {
			fixtures = append(fixtures, fixtureObject{s.RestType, s.OID, s.Path})
		}
	}
	present := func(restType, oid string) (bool, error) {
		_, err := c.Get(ctx, restType, oid, client.GetOptions{Options: []string{"noFetch", "raw"}})
		if errors.Is(err, client.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	for _, f := range fixtures {
		if ok, err := present(f.restType, f.oid); err != nil || ok {
			t.Fatalf("fixture object %s/%s already exists on the server (err %v); remove it before running this test", f.restType, f.oid, err)
		}
	}
	rawGet := func(restType, oid string) map[string]any {
		t.Helper()
		obj, err := c.Get(ctx, restType, oid, client.GetOptions{Options: []string{"noFetch", "raw"}})
		if err != nil {
			t.Fatalf("reading %s/%s: %v", restType, oid, err)
		}
		return obj.Body
	}
	builtin := rawGet("archetypes", accConvertBuiltinOID)

	// Cleanup however the test ends: delete what was seeded (by OID, raw:
	// never a built-in), restore the built-in snapshot, and check that
	// nothing of the fixture remains.
	t.Cleanup(func() {
		for i := len(fixtures) - 1; i >= 0; i-- {
			f := fixtures[i]
			if err := c.Delete(ctx, f.restType, f.oid, []string{"raw"}); err != nil && !errors.Is(err, client.ErrNotFound) {
				t.Errorf("deleting %s/%s: %v", f.restType, f.oid, err)
			}
		}
		if _, err := c.Create(ctx, client.Object{Type: "archetypes", Body: accConvertRestorable(builtin).(map[string]any)}, []string{"overwrite", "raw"}); err != nil {
			t.Errorf("restoring the built-in archetype %s: %v", accConvertBuiltinOID, err)
		}
		for _, f := range fixtures {
			if ok, err := present(f.restType, f.oid); err != nil || ok {
				t.Errorf("fixture object %s/%s remains on the server (err %v)", f.restType, f.oid, err)
			}
		}
	})

	// 1. Seed the catalogue as midPoint would hold it: each non-built-in
	// file's raw JSON, created like an import. An object whose references
	// do not exist yet is retried after the others.
	pending := slices.Clone(fixtures)
	for len(pending) > 0 {
		var failed []fixtureObject
		var errs []string
		for _, f := range pending {
			raw, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			var wrapped map[string]any
			if err := json.Unmarshal(raw, &wrapped); err != nil || len(wrapped) != 1 {
				t.Fatalf("%s: not one wrapped object (err %v)", f.path, err)
			}
			var body map[string]any
			for _, v := range wrapped {
				body, _ = v.(map[string]any)
			}
			options := []string{"isImport"}
			if f.restType == "resources" {
				options = append(options, "reevaluateSearchFilters")
			}
			if _, err := c.Create(ctx, client.Object{Type: f.restType, Body: body}, options); err != nil {
				failed = append(failed, f)
				errs = append(errs, fmt.Sprintf("%s: %v", f.path, err))
			}
		}
		if len(failed) == len(pending) {
			t.Fatalf("cannot seed the fixture:\n%s", strings.Join(errs, "\n"))
		}
		pending = failed
	}
	// midPoint holds the structured filters (the test is about them).
	role := rawGet("roles", accConvertRoleOID)
	inducement := accConvertOneValue(t, role["inducement"])
	if f, _ := inducement["targetRef"].(map[string]any)["filter"].(map[string]any); f == nil || f["text"] != nil {
		t.Fatalf("seeded inducement filter = %v, want a structured filter", inducement["targetRef"])
	}
	connectorRef, _ := rawGet("resources", accConvertResourceOID)["connectorRef"].(map[string]any)
	if f, _ := connectorRef["filter"].(map[string]any); f == nil || f["text"] != nil {
		t.Fatalf("seeded connectorRef = %v, want a structured filter", connectorRef)
	}

	// 2. Export every catalogue object from midPoint, one wrapped JSON file
	// each, and convert the export.
	exportDir := t.TempDir()
	for _, s := range srcs {
		var opts []string
		if s.RestType == "resources" {
			opts = []string{"noFetch"} // no connector call
		}
		obj, err := c.Get(ctx, s.RestType, s.OID, client.GetOptions{Options: opts})
		if err != nil {
			t.Fatalf("exporting %s/%s: %v", s.RestType, s.OID, err)
		}
		b, err := json.MarshalIndent(client.Wrap(*obj), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(exportDir, s.RestType, s.OID+".json"), b)
	}
	files, _, err := convert.Convert([]string{exportDir}, convert.Options{Format: "hcl"})
	if err != nil {
		t.Fatal(err)
	}
	env := accBuildProvider(t)
	convDir := t.TempDir()
	for name, b := range files {
		writeFile(t, filepath.Join(convDir, name), b)
	}
	writeFile(t, filepath.Join(convDir, "provider.tf"), []byte(accProviderBlock))
	conv := newAccTerraform(t, env, convDir)
	conv.run("init", "-input=false", "-no-color")

	// versions is the version of every catalogue object.
	versions := func() map[string]any {
		out := map[string]any{}
		for _, s := range srcs {
			out[s.RestType+"/"+s.OID] = rawGet(s.RestType, s.OID)["version"]
		}
		return out
	}
	seeded := versions()

	// 3. The plan: every catalogue object is an import with no change.
	plan := accConvertPlan(t, conv)
	var imported []string
	for _, rc := range plan.ResourceChanges {
		ok := rc.Mode == "managed" && len(rc.Change.Importing) > 0 && slices.Equal(rc.Change.Actions, []string{"no-op"})
		if !ok {
			t.Errorf("%s: actions %v, importing %t, want an import with no change%s",
				rc.Address, rc.Change.Actions, len(rc.Change.Importing) > 0, attrDiff(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown))
			continue
		}
		imported = append(imported, rc.Address)
	}
	if len(imported) != len(srcs) {
		t.Errorf("imported %d objects %v, the catalogue has %d", len(imported), imported, len(srcs))
	}
	// The data lookups resolved at plan time: they are in the prior state.
	var lookups []string
	if plan.PriorState.Values != nil {
		for _, r := range plan.PriorState.Values.RootModule.Resources {
			if r.Mode == "data" {
				lookups = append(lookups, r.Address)
			}
		}
	}
	sort.Strings(lookups)
	if want := dataBlocks(t, files); !slices.Equal(lookups, want) {
		t.Errorf("resolved data lookups %v, want %v", lookups, want)
	}
	if t.Failed() {
		t.Fatalf("the plan:\n%s", conv.run("show", "-no-color", "plan.tfplan"))
	}

	// 4. Apply the imports: no object changes, the built-in included.
	conv.run("apply", "-input=false", "-no-color", "plan.tfplan")
	if got := versions(); !reflect.DeepEqual(got, seeded) {
		t.Errorf("applying the imports changed objects: versions %v, want %v", got, seeded)
	}

	// 5. Remove the import blocks: the plan is empty.
	if err := os.Remove(filepath.Join(convDir, "imports.tf")); err != nil {
		t.Fatal(err)
	}
	accConvertExpectEmptyPlan(t, conv)

	// 6. Edit one description on the role with the structured filter
	// inducement: midPoint changes that description and nothing else.
	rolesTF := filepath.Join(convDir, "roles.tf")
	hcl, err := os.ReadFile(rolesTF)
	if err != nil {
		t.Fatal(err)
	}
	const oldDesc, newDesc = `"Dashboards for engineers."`, `"Dashboards for engineers, edited after the migration."`
	if n := strings.Count(string(hcl), oldDesc); n != 1 {
		t.Fatalf("roles.tf has %d descriptions %s, want 1", n, oldDesc)
	}
	writeFile(t, rolesTF, []byte(strings.Replace(string(hcl), oldDesc, newDesc, 1)))
	before := map[string]map[string]any{}
	for _, s := range srcs {
		before[s.RestType+"/"+s.OID] = rawGet(s.RestType, s.OID)
	}
	plan = accConvertPlan(t, conv)
	for _, rc := range plan.ResourceChanges {
		want := []string{"no-op"}
		if rc.Address == "midpoint_role.observability_engineer" {
			want = []string{"update"}
		}
		if !slices.Equal(rc.Change.Actions, want) {
			t.Errorf("%s: actions %v, want %v%s", rc.Address, rc.Change.Actions, want, attrDiff(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown))
		}
	}
	if t.Failed() {
		t.Fatalf("the plan:\n%s", conv.run("show", "-no-color", "plan.tfplan"))
	}
	conv.run("apply", "-input=false", "-no-color", "plan.tfplan")
	for _, s := range srcs {
		key := s.RestType + "/" + s.OID
		after := rawGet(s.RestType, s.OID)
		if s.OID != accConvertRoleOID {
			if !reflect.DeepEqual(after["version"], before[key]["version"]) {
				t.Errorf("%s changed (version %v to %v) by an edit of another object", key, before[key]["version"], after["version"])
			}
			continue
		}
		b := normalize.Canonical(s.RestType, before[key])
		a := normalize.Canonical(s.RestType, after)
		var changed []string
		keys := map[string]bool{}
		for k := range b {
			keys[k] = true
		}
		for k := range a {
			keys[k] = true
		}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			if !reflect.DeepEqual(a[k], b[k]) {
				changed = append(changed, k)
			}
		}
		if !slices.Equal(changed, []string{"description"}) || a["description"] != strings.Trim(newDesc, `"`) {
			t.Errorf("the role changed in %v (description %q), want the description alone", changed, a["description"])
		}
		if got := accConvertOneValue(t, after["inducement"]); !reflect.DeepEqual(got, inducement) {
			t.Errorf("the inducement changed:\n%v\nwant it as seeded:\n%v", got, inducement)
		}
	}
	if got := rawGet("resources", accConvertResourceOID)["connectorRef"]; !reflect.DeepEqual(got, connectorRef) {
		t.Errorf("the connectorRef changed to %v, want it as seeded %v", got, connectorRef)
	}
	accConvertExpectEmptyPlan(t, conv)
}

// prismTypesNamespace is the namespace midPoint declares ("@ns") on maps
// that hold prism annotations such as "@id" and "@metadata".
const prismTypesNamespace = "http://prism.evolveum.com/xml/ns/public/types-3"

// accConvertRestorable is a copy of v, an object body as midPoint returned
// it, that midPoint accepts back: without the "@ns" declarations of the
// prism namespace, which midPoint requires before any other field (a Go map
// is marshalled with sorted keys, "@id" and "@metadata" first) and which
// only declare the default namespace of the annotations.
func accConvertRestorable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if k == "@ns" && e == prismTypesNamespace {
				continue
			}
			out[k] = accConvertRestorable(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = accConvertRestorable(e)
		}
		return out
	default:
		return v
	}
}

// accConvertOneValue is the one value of a multi-valued item midPoint
// returned (a map, or a one-element list of it).
func accConvertOneValue(t *testing.T, v any) map[string]any {
	t.Helper()
	if l, ok := v.([]any); ok {
		if len(l) != 1 {
			t.Fatalf("%d values, want one: %v", len(l), v)
		}
		v = l[0]
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not one container value: %v", v)
	}
	return m
}

// accConvertPlan plans dir into plan.tfplan and returns it.
func accConvertPlan(t *testing.T, tf *accTerraform) tfPlan {
	t.Helper()
	tf.run("plan", "-input=false", "-no-color", "-out", "plan.tfplan")
	var plan tfPlan
	if err := json.Unmarshal(tf.run("show", "-json", "-no-color", "plan.tfplan"), &plan); err != nil {
		t.Fatalf("parsing the plan: %v", err)
	}
	return plan
}

// accConvertExpectEmptyPlan fails unless every resource plans no change.
func accConvertExpectEmptyPlan(t *testing.T, tf *accTerraform) {
	t.Helper()
	plan := accConvertPlan(t, tf)
	clean := true
	for _, rc := range plan.ResourceChanges {
		if !slices.Equal(rc.Change.Actions, []string{"no-op"}) && !slices.Equal(rc.Change.Actions, []string{"read"}) {
			clean = false
			t.Errorf("%s: actions %v, want no change%s", rc.Address, rc.Change.Actions, attrDiff(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown))
		}
	}
	if !clean {
		t.Fatalf("the plan:\n%s", tf.run("show", "-no-color", "plan.tfplan"))
	}
}

// attrDiff lists the top-level attributes whose value differs between a
// change's before and after, one per line with both values as JSON; a
// value known only after apply is "(known after apply)".
func attrDiff(before, after, afterUnknown any) string {
	b, _ := before.(map[string]any)
	a, _ := after.(map[string]any)
	a = maps.Clone(a)
	if u, ok := afterUnknown.(map[string]any); ok {
		for k, v := range u {
			if v == true {
				a[k] = "(known after apply)"
			}
		}
	}
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	var out strings.Builder
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if reflect.DeepEqual(b[k], a[k]) {
			continue
		}
		bj, _ := json.Marshal(b[k])
		aj, _ := json.Marshal(a[k])
		fmt.Fprintf(&out, "\n    %s:\n      before: %s\n      after:  %s", k, bj, aj)
	}
	return out.String()
}

// dataBlocks lists the data source addresses the converted files declare,
// sorted, from their `data "<type>" "<name>"` lines.
func dataBlocks(t *testing.T, files map[string][]byte) []string {
	t.Helper()
	var out []string
	for _, b := range files {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 4 && f[0] == "data" && f[3] == "{" {
				out = append(out, "data."+strings.Trim(f[1], `"`)+"."+strings.Trim(f[2], `"`))
			}
		}
	}
	sort.Strings(out)
	return out
}

// writeFile writes b to name, creating its directory.
func writeFile(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
