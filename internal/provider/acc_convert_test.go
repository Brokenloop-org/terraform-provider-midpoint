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
)

// accConvertKinds maps the typed resource kinds to their REST type; a
// midpoint_object records its type in state.
var accConvertKinds = map[string]string{
	"midpoint_archetype": "archetypes", "midpoint_mark": "marks", "midpoint_object_template": "objectTemplates",
	"midpoint_org": "orgs", "midpoint_resource": "resources", "midpoint_role": "roles",
	"midpoint_service": "services", "midpoint_task": "tasks",
}

// accConvertRestType is the REST type of a managed resource in state.
func accConvertRestType(r tfShowResource) string {
	if r.Type == "midpoint_object" {
		typ, _ := r.Values["type"].(string)
		return typ
	}
	return accConvertKinds[r.Type]
}

// TestAccConvert_roundTrip applies the converter's golden HCL, exports every
// object it created, converts the export and checks that terraform plans
// the converted configuration as imports with no change: what the converter
// writes from a server's objects matches what the provider reads back.
func TestAccConvert_roundTrip(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	ctx := context.Background()

	// The fixture objects: the only ones besides the state's own resources
	// this test may touch, and never a built-in.
	catalogue := filepath.Join("..", "convert", "testdata", "catalogue")
	srcs, _, err := convert.Load([]string{catalogue})
	if err != nil {
		t.Fatal(err)
	}
	type fixtureObject struct{ restType, oid string }
	var fixtures []fixtureObject
	for _, s := range srcs {
		if !client.IsBuiltinOID(s.OID) {
			fixtures = append(fixtures, fixtureObject{s.RestType, s.OID})
		}
	}
	present := func(f fixtureObject) (bool, error) {
		_, err := c.Get(ctx, f.restType, f.oid, client.GetOptions{Options: []string{"noFetch", "raw"}})
		if errors.Is(err, client.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	for _, f := range fixtures {
		if ok, err := present(f); err != nil || ok {
			t.Fatalf("fixture object %s/%s already exists on the server (err %v); remove it before running this test", f.restType, f.oid, err)
		}
	}

	env := accBuildProvider(t)

	// 1. Apply the golden HCL without its import blocks: nothing exists yet,
	// and the built-ins are adopted (adopt_existing).
	origDir := t.TempDir()
	golden := filepath.Join("..", "convert", "testdata", "golden")
	entries, err := os.ReadDir(golden)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "imports.tf" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(golden, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(origDir, e.Name()), b)
	}
	writeFile(t, filepath.Join(origDir, "provider.tf"), []byte(accProviderBlock))
	orig := newAccTerraform(t, env, origDir)
	orig.run("init", "-input=false", "-no-color")

	// 4. Destroy the originals however the test ends. Adopted built-ins
	// leave the state first: the provider refuses to delete them, and they
	// must stay. Then any fixture object still present is deleted by OID.
	t.Cleanup(func() {
		resources, err := orig.stateResources()
		if err != nil {
			t.Errorf("reading the state to destroy: %v", err)
		}
		for _, r := range resources {
			if r.Mode == "managed" && r.Values["adopt_existing"] == true {
				if _, err := orig.try("state", "rm", "-no-color", r.Address); err != nil {
					t.Errorf("removing adopted %s from the state: %v", r.Address, err)
				}
			}
		}
		if _, err := orig.try("destroy", "-auto-approve", "-no-color"); err != nil {
			t.Errorf("destroy: %v", err)
		}
		for _, f := range fixtures {
			ok, err := present(f)
			if err != nil {
				t.Errorf("checking %s/%s: %v", f.restType, f.oid, err)
				continue
			}
			if !ok {
				continue
			}
			t.Errorf("%s/%s is still on the server after destroy; deleting it", f.restType, f.oid)
			if err := c.Delete(ctx, f.restType, f.oid, nil); err != nil {
				t.Errorf("deleting %s/%s: %v", f.restType, f.oid, err)
			}
		}
		// 5. Nothing of the fixture remains.
		for _, f := range fixtures {
			if ok, err := present(f); err != nil || ok {
				t.Errorf("fixture object %s/%s remains on the server (err %v)", f.restType, f.oid, err)
			}
		}
	})

	orig.run("apply", "-auto-approve", "-input=false", "-no-color")

	// 2. Export every object the apply manages, one wrapped JSON file each.
	resources, err := orig.stateResources()
	if err != nil {
		t.Fatal(err)
	}
	exportDir := t.TempDir()
	var managed []string
	for _, r := range resources {
		if r.Mode != "managed" {
			continue
		}
		restType := accConvertRestType(r)
		oid, _ := r.Values["oid"].(string)
		if restType == "" || oid == "" {
			t.Fatalf("%s: no REST type or oid in state", r.Address)
		}
		var opts []string
		if restType == "resources" {
			opts = []string{"noFetch"} // no connector call
		}
		obj, err := c.Get(ctx, restType, oid, client.GetOptions{Options: opts})
		if err != nil {
			t.Fatalf("exporting %s: %v", r.Address, err)
		}
		b, err := json.MarshalIndent(client.Wrap(*obj), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(exportDir, restType, oid+".json"), b)
		managed = append(managed, r.Address)
	}
	if len(managed) != len(srcs) {
		t.Errorf("the apply manages %d objects, the catalogue has %d", len(managed), len(srcs))
	}

	// 3. Convert the export and plan the result with its import blocks.
	files, _, err := convert.Convert([]string{exportDir}, convert.Options{Format: "hcl"})
	if err != nil {
		t.Fatal(err)
	}
	convDir := t.TempDir()
	for name, b := range files {
		writeFile(t, filepath.Join(convDir, name), b)
	}
	writeFile(t, filepath.Join(convDir, "provider.tf"), []byte(accProviderBlock))
	conv := newAccTerraform(t, env, convDir)
	conv.run("init", "-input=false", "-no-color")
	conv.run("plan", "-input=false", "-no-color", "-out", "plan.tfplan")
	var plan tfPlan
	if err := json.Unmarshal(conv.run("show", "-json", "-no-color", "plan.tfplan"), &plan); err != nil {
		t.Fatalf("parsing the plan: %v", err)
	}

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
	sort.Strings(imported)
	sort.Strings(managed)
	if !slices.Equal(imported, managed) {
		t.Errorf("imported %v\nwant every applied object %v", imported, managed)
	}
	if t.Failed() {
		// Terraform's own rendering of the plan shows the nested differences.
		t.Logf("the plan:\n%s", conv.run("show", "-no-color", "plan.tfplan"))
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
	if want := dataBlocks(t, files); !slices.Equal(lookups, want) {
		t.Errorf("resolved data lookups %v, want %v", lookups, want)
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
