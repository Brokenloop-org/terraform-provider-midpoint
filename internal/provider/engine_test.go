package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpstub"
)

const (
	engRoleOID      = "11111111-1111-1111-1111-111111111111"
	engOtherRoleOID = "22222222-2222-2222-2222-222222222222"
	engArchetypeOID = "00000000-0000-0000-0000-000000000329"
	engResourceOID  = "33333333-3333-3333-3333-333333333333"
)

var testRoleSpec = engineSpec{
	RestType:      "roles",
	ShortName:     "role",
	CreateOptions: []string{"isImport", "raw"},
	UpdateOptions: []string{"raw"},
	MembersCheck:  roleMembersCheck,
}

var testResourceSpec = engineSpec{
	RestType:      "resources",
	ShortName:     "resource",
	CreateOptions: []string{"isImport", "reevaluateSearchFilters"},
	UpdateOptions: []string{"reevaluateSearchFilters"},
	ReadOptions:   []string{"noFetch"},
	MembersCheck:  shadowsCheck,
}

func testEngine(t *testing.T, spec engineSpec, defaults map[string][]string) (*engine, *mpstub.Server) {
	t.Helper()
	s := mpstub.New()
	t.Cleanup(s.Close)
	c, err := client.New(client.Config{URL: s.URL(), Username: "administrator", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return &engine{c: c, defaults: defaults, spec: spec}, s
}

// requests returns the recorded requests with the given method.
func requests(s *mpstub.Server, method string) []mpstub.Recorded {
	var out []mpstub.Recorded
	for _, r := range s.Requests() {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

func diagText(diags diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary() + ": " + d.Detail() + "\n")
	}
	return b.String()
}

func noErrors(t *testing.T, diags diag.Diagnostics) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("unexpected errors:\n%s", diagText(diags))
	}
}

// asJSON round-trips v through JSON so that Go literals and decoded bodies
// compare alike.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func archetypeAssignment(oid string) map[string]any {
	return map[string]any{"targetRef": map[string]any{"oid": oid, "type": "ArchetypeType"}}
}

func roleInducement(oid string) map[string]any {
	return map[string]any{"targetRef": map[string]any{"oid": oid, "type": "RoleType"}}
}

// seededRole is a role as a configuration would state it.
func seededRole() map[string]any {
	return map[string]any{
		"oid":         engRoleOID,
		"name":        "r1",
		"description": "d",
		"assignment":  []any{archetypeAssignment(engArchetypeOID)},
		"inducement":  []any{roleInducement(engOtherRoleOID)},
	}
}

// desiredRole is seededRole as a resource hands it to the engine: no oid
// in the body (it travels in objectState.OID).
func desiredRole() map[string]any {
	d := seededRole()
	delete(d, "oid")
	return d
}

func TestEngineCreate_newObjectSendsOptions(t *testing.T) {
	tests := []struct {
		name        string
		oid         string
		defaults    map[string][]string
		options     []string
		wantOptions []string
	}{
		{name: "builtin options", wantOptions: []string{"isImport", "raw"}},
		{name: "explicit oid not yet present", oid: engRoleOID, wantOptions: []string{"isImport", "raw"}},
		{name: "provider default wins over builtin", defaults: map[string][]string{"role": {"isImport"}, "org": {"raw"}}, wantOptions: []string{"isImport"}},
		{name: "resource options win over default", defaults: map[string][]string{"role": {"isImport"}}, options: []string{"raw"}, wantOptions: []string{"raw"}},
		{name: "empty resource options send none", options: []string{}, wantOptions: nil},
		{name: "overwrite only when the user lists it", options: []string{"overwrite"}, wantOptions: []string{"overwrite"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, tt.defaults)
			oid, adopted, diags := e.create(context.Background(), objectState{OID: tt.oid, Options: tt.options, Body: map[string]any{"name": "r1", "inducement": []any{}}})
			noErrors(t, diags)
			if adopted {
				t.Fatal("adopted a new object")
			}
			if tt.oid != "" && oid != tt.oid {
				t.Fatalf("oid = %q, want %q", oid, tt.oid)
			}
			posts := requests(s, http.MethodPost)
			if len(posts) != 1 {
				t.Fatalf("sent %d POSTs, want 1", len(posts))
			}
			if got := posts[0].Query["options"]; !reflect.DeepEqual(got, tt.wantOptions) {
				t.Fatalf("options = %v, want %v", got, tt.wantOptions)
			}
			want := map[string]any{"role": map[string]any{"name": "r1"}}
			if tt.oid != "" {
				want["role"].(map[string]any)["oid"] = tt.oid
			}
			if got := asJSON(t, posts[0].Body); !reflect.DeepEqual(got, asJSON(t, want)) {
				t.Fatalf("POST body = %v, want %v (empty items pruned, oid in body)", got, want)
			}
			if stored := s.Get("roles", oid); stored == nil || stored["name"] != "r1" {
				t.Fatalf("stored = %v", stored)
			}
		})
	}
}

func TestEngineCreate_filterRefAddsReevaluate(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	body := map[string]any{
		"name":       "r1",
		"inducement": []any{map[string]any{"targetRef": map[string]any{"type": "RoleType", "filter": map[string]any{"text": `name = "x"`}}}},
	}
	_, _, diags := e.create(context.Background(), objectState{Body: body})
	noErrors(t, diags)
	posts := requests(s, http.MethodPost)
	if len(posts) != 1 {
		t.Fatalf("sent %d POSTs, want 1", len(posts))
	}
	if got, want := posts[0].Query["options"], []string{"isImport", "raw", "reevaluateSearchFilters"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %v, want %v", got, want)
	}
}

func TestEngineCreate_existingOIDFails(t *testing.T) {
	tests := []struct {
		name string
		st   objectState
	}{
		{name: "oid attribute", st: objectState{OID: engRoleOID, Body: map[string]any{"name": "changed"}}},
		{name: "oid only in the body", st: objectState{Body: map[string]any{"oid": engRoleOID, "name": "changed"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, nil)
			s.Put("roles", seededRole())
			before := s.Get("roles", engRoleOID)

			_, adopted, diags := e.create(context.Background(), tt.st)
			if !diags.HasError() || adopted {
				t.Fatalf("create of an existing OID: adopted=%v diags=%s", adopted, diagText(diags))
			}
			want := "object roles/" + engRoleOID + " already exists; import it or set adopt_existing = true"
			if !strings.Contains(diagText(diags), want) {
				t.Fatalf("diagnostics = %s, want %q", diagText(diags), want)
			}
			for _, r := range s.Requests() {
				if r.Method != http.MethodGet {
					t.Fatalf("sent %s %s; only a read is allowed before failing", r.Method, r.Path)
				}
			}
			if got := s.Get("roles", engRoleOID); !reflect.DeepEqual(got, before) {
				t.Fatalf("object changed: %v", got)
			}
		})
	}
}

func TestEngineCreate_adoptExisting(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.Put("roles", seededRole())
	oid, adopted, diags := e.create(context.Background(), objectState{OID: engRoleOID, AdoptExisting: true, Body: map[string]any{"name": "changed"}})
	noErrors(t, diags)
	if !adopted || oid != engRoleOID {
		t.Fatalf("adopted=%v oid=%q, want adopted %s", adopted, oid, engRoleOID)
	}
	if n := len(requests(s, http.MethodPost)) + len(requests(s, http.MethodPatch)); n != 0 {
		t.Fatalf("adopt sent %d writes; the caller applies the update", n)
	}
	if got := s.Get("roles", engRoleOID)["name"]; got != "r1" {
		t.Fatalf("name = %v, adopt must not write", got)
	}
}

func TestEngineRead_canonical(t *testing.T) {
	e, s := testEngine(t, testResourceSpec, nil)
	s.Put("resources", map[string]any{"oid": engResourceOID, "name": "ldap", "description": "x"})
	s.Put("roles", seededRole())

	actual, found, diags := e.read(context.Background(), engResourceOID)
	noErrors(t, diags)
	if !found {
		t.Fatal("not found")
	}
	if got, want := asJSON(t, actual), asJSON(t, map[string]any{"oid": engResourceOID, "name": "ldap", "description": "x"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("read = %v, want %v", got, want)
	}
	gets := requests(s, http.MethodGet)
	if len(gets) != 1 {
		t.Fatalf("sent %d GETs, want 1", len(gets))
	}
	if got := gets[0].Query["options"]; !reflect.DeepEqual(got, []string{"noFetch"}) {
		t.Errorf("read options = %v, want [noFetch]", got)
	}
	wantExclude := []string{"metadata", "operationExecution", "roleMembershipRef", "linkRef", "fetchResult", "operationalStateHistory"}
	if got := gets[0].Query["exclude"]; !reflect.DeepEqual(got, wantExclude) {
		t.Errorf("exclude = %v, want %v", got, wantExclude)
	}

	// A role read through all the stamped noise keeps only configuration
	// and container ids.
	re := &engine{c: e.c, spec: testRoleSpec}
	role, found, diags := re.read(context.Background(), engRoleOID)
	noErrors(t, diags)
	if !found {
		t.Fatal("role not found")
	}
	for _, k := range []string{"metadata", "@metadata", "version", "iteration", "iterationToken", "archetypeRef", "roleMembershipRef", "activation"} {
		if _, ok := role[k]; ok {
			t.Errorf("canonical read keeps %s", k)
		}
	}
	a := role["assignment"].([]any)[0].(map[string]any)
	if _, ok := a["@id"]; !ok {
		t.Errorf("canonical read lost the container @id: %v", a)
	}
	for _, k := range []string{"@ns", "@metadata", "activation"} {
		if _, ok := a[k]; ok {
			t.Errorf("assignment keeps %s", k)
		}
	}
	if ref := a["targetRef"].(map[string]any); ref["type"] != "ArchetypeType" || ref["targetName"] != nil {
		t.Errorf("targetRef = %v", ref)
	}

	_, found, diags = e.read(context.Background(), "missing")
	noErrors(t, diags)
	if found {
		t.Fatal("missing object reported found")
	}
}

func TestEngineReconcile(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.Put("roles", seededRole())
	actual, _, diags := e.read(context.Background(), engRoleOID)
	noErrors(t, diags)

	tests := []struct {
		name      string
		edit      func(d map[string]any)
		wantEqual bool
		wantPaths []string
	}{
		{name: "configuration equals the noisy server copy", edit: func(map[string]any) {}, wantEqual: true},
		{name: "empty items in desired are pruned", edit: func(d map[string]any) { d["authorization"] = []any{}; d["extension"] = map[string]any{} }, wantEqual: true},
		{name: "changed description", edit: func(d map[string]any) { d["description"] = "new" }, wantPaths: []string{"replace description"}},
		{name: "removed item", edit: func(d map[string]any) { delete(d, "description") }, wantPaths: []string{"replace description"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := desiredRole()
			tt.edit(desired)
			equal, deltas, err := e.reconcile(context.Background(), desired, actual)
			if err != nil {
				t.Fatal(err)
			}
			if equal != tt.wantEqual {
				t.Fatalf("equal = %v, want %v (deltas %v)", equal, tt.wantEqual, deltas)
			}
			var paths []string
			for _, d := range deltas {
				paths = append(paths, d.ModificationType+" "+d.Path)
			}
			if !reflect.DeepEqual(paths, tt.wantPaths) {
				t.Fatalf("deltas = %v, want %v", paths, tt.wantPaths)
			}
		})
	}
}

func TestEngineUpdate_noDeltaNoPatch(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.Put("roles", seededRole())
	noErrors(t, e.update(context.Background(), engRoleOID, desiredRole(), nil))
	if p := requests(s, http.MethodPatch); len(p) != 0 {
		t.Fatalf("sent %d PATCHes for an unchanged object: %+v", len(p), p)
	}
}

func TestEngineUpdate_sendsOnlyChangedItems(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.Put("roles", seededRole())
	desired := desiredRole()
	desired["description"] = "new"
	desired["inducement"] = append(desired["inducement"].([]any), roleInducement(engArchetypeOID))

	noErrors(t, e.update(context.Background(), engRoleOID, desired, nil))
	patches := requests(s, http.MethodPatch)
	if len(patches) != 1 {
		t.Fatalf("sent %d PATCHes, want 1", len(patches))
	}
	if got := patches[0].Query["options"]; !reflect.DeepEqual(got, []string{"raw"}) {
		t.Errorf("update options = %v, want [raw]", got)
	}
	want := map[string]any{"objectModification": map[string]any{"itemDelta": []any{
		map[string]any{"modificationType": "replace", "path": "description", "value": "new"},
		map[string]any{"modificationType": "add", "path": "inducement", "value": []any{
			map[string]any{"targetRef": map[string]any{"oid": engArchetypeOID, "type": "RoleType", "relation": "org:default"}},
		}},
	}}}
	if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, asJSON(t, want)) {
		t.Fatalf("PATCH body = %v\nwant %v", got, want)
	}
	// The stub applied it: a second update has nothing to send.
	noErrors(t, e.update(context.Background(), engRoleOID, desired, nil))
	if n := len(requests(s, http.MethodPatch)); n != 1 {
		t.Fatalf("second update sent a PATCH (%d total)", n)
	}
}

func TestEngineUpdate_filterRefAddsReevaluate(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		want    []string
	}{
		{name: "added to builtin", want: []string{"raw", "reevaluateSearchFilters"}},
		{name: "added to empty resource options", options: []string{}, want: []string{"reevaluateSearchFilters"}},
		{name: "not duplicated", options: []string{"reevaluateSearchFilters"}, want: []string{"reevaluateSearchFilters"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, nil)
			s.Put("roles", seededRole())
			desired := desiredRole()
			desired["inducement"] = append(desired["inducement"].([]any),
				map[string]any{"targetRef": map[string]any{"type": "RoleType", "filter": map[string]any{"text": `name = "x"`}}})
			noErrors(t, e.update(context.Background(), engRoleOID, desired, tt.options))
			patches := requests(s, http.MethodPatch)
			if len(patches) != 1 {
				t.Fatalf("sent %d PATCHes, want 1", len(patches))
			}
			if got := patches[0].Query["options"]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("options = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEngineUpdate_missingObjectFails(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	diags := e.update(context.Background(), engRoleOID, desiredRole(), nil)
	if !diags.HasError() || !strings.Contains(diagText(diags), "roles/"+engRoleOID) {
		t.Fatalf("diagnostics = %s, want an error naming the object", diagText(diags))
	}
	if n := len(requests(s, http.MethodPatch)); n != 0 {
		t.Fatalf("sent %d PATCHes", n)
	}
}

func TestEngineDelete_refusesWithMembers(t *testing.T) {
	tests := []struct {
		name       string
		spec       engineSpec
		restType   string
		oid        string
		totals     bool
		seed       func(s *mpstub.Server)
		wantSearch string
		wantMsg    string
	}{
		{
			name: "role members without a reported total", spec: testRoleSpec, restType: "roles", oid: engRoleOID,
			seed: func(s *mpstub.Server) {
				s.Put("users", map[string]any{"name": "alice", "assignment": roleInducement(engRoleOID)})
				s.Put("orgs", map[string]any{"name": map[string]any{"orig": "org1", "norm": "org1"}, "assignment": roleInducement(engRoleOID)})
			},
			wantSearch: "/assignmentHolders/search",
			wantMsg:    "cannot delete roles/" + engRoleOID + ": at least 2 objects still reference it (first: alice, org1); set force_destroy = true to delete anyway",
		},
		{
			name: "role members with a reported total", spec: testRoleSpec, restType: "roles", oid: engRoleOID, totals: true,
			seed: func(s *mpstub.Server) {
				for _, n := range []string{"u1", "u2", "u3", "u4", "u5", "u6"} {
					s.Put("users", map[string]any{"name": n, "assignment": roleInducement(engRoleOID)})
				}
			},
			wantSearch: "/assignmentHolders/search",
			wantMsg:    "cannot delete roles/" + engRoleOID + ": 6 objects still reference it (first: u1, u2, u3, u4, u5); set force_destroy = true to delete anyway",
		},
		{
			name: "resource shadows", spec: testResourceSpec, restType: "resources", oid: engResourceOID,
			seed: func(s *mpstub.Server) {
				s.Put("shadows", map[string]any{"name": "uid=a", "resourceRef": map[string]any{"oid": engResourceOID, "type": "ResourceType"}})
			},
			wantSearch: "/shadows/search",
			wantMsg:    "cannot delete resources/" + engResourceOID + ": at least 1 objects still reference it (first: uid=a); set force_destroy = true to delete anyway",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s := testEngine(t, tt.spec, nil)
			s.Put(tt.restType, map[string]any{"oid": tt.oid, "name": "target"})
			tt.seed(s)
			s.ReportTotals(tt.totals)

			diags := e.delete(context.Background(), tt.oid, false)
			if !diags.HasError() || !strings.Contains(diagText(diags), tt.wantMsg) {
				t.Fatalf("diagnostics = %s\nwant %q", diagText(diags), tt.wantMsg)
			}
			if n := len(requests(s, http.MethodDelete)); n != 0 {
				t.Fatalf("sent %d DELETEs", n)
			}
			if s.Get(tt.restType, tt.oid) == nil {
				t.Fatal("object deleted")
			}
			searches := requests(s, http.MethodPost)
			if len(searches) != 1 || searches[0].Path != tt.wantSearch || !slices.Contains(searches[0].Query["options"], "raw") {
				t.Fatalf("searches = %+v, want one raw search at %s", searches, tt.wantSearch)
			}
		})
	}
}

func TestEngineDelete_noMembersDeletes(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.Put("roles", seededRole())
	noErrors(t, e.delete(context.Background(), engRoleOID, false))
	if s.Get("roles", engRoleOID) != nil {
		t.Fatal("object not deleted")
	}
	if n := len(requests(s, http.MethodPost)); n != 1 {
		t.Fatalf("sent %d searches, want 1", n)
	}
}

func TestEngineDelete_forceDestroy(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	s.Put("roles", seededRole())
	s.Put("users", map[string]any{"name": "alice", "assignment": roleInducement(engRoleOID)})
	noErrors(t, e.delete(context.Background(), engRoleOID, true))
	if s.Get("roles", engRoleOID) != nil {
		t.Fatal("object not deleted")
	}
	if n := len(requests(s, http.MethodPost)); n != 0 {
		t.Fatalf("force_destroy still searched (%d searches)", n)
	}
}

func TestEngineDelete_notFoundIsSuccess(t *testing.T) {
	for _, force := range []bool{false, true} {
		e, _ := testEngine(t, testRoleSpec, nil)
		noErrors(t, e.delete(context.Background(), engRoleOID, force))
	}
}

func TestEngineDelete_noMembersCheck(t *testing.T) {
	spec := testRoleSpec
	spec.MembersCheck = nil
	e, s := testEngine(t, spec, nil)
	s.Put("roles", seededRole())
	s.Put("users", map[string]any{"name": "alice", "assignment": roleInducement(engRoleOID)})
	noErrors(t, e.delete(context.Background(), engRoleOID, false))
	if s.Get("roles", engRoleOID) != nil {
		t.Fatal("object not deleted")
	}
}

func TestEngineReadInto(t *testing.T) {
	tests := []struct {
		name         string
		seed         bool
		edit         func(d map[string]any)
		afterWrite   bool
		wantFound    bool
		wantCallback bool
		wantWarning  string
	}{
		{name: "equal keeps configured state", seed: true, edit: func(map[string]any) {}, wantFound: true},
		{name: "equal after write keeps configured state", seed: true, edit: func(map[string]any) {}, afterWrite: true, wantFound: true},
		{name: "drift on refresh surfaces the server copy", seed: true, edit: func(d map[string]any) { d["description"] = "configured" }, wantFound: true, wantCallback: true},
		{
			name: "difference after write warns", seed: true, afterWrite: true, wantFound: true,
			edit:        func(d map[string]any) { d["description"] = "configured"; d["riskLevel"] = "high" },
			wantWarning: "midPoint normalized description, riskLevel; the next plan will show this difference",
		},
		{name: "missing object", edit: func(map[string]any) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s := testEngine(t, testRoleSpec, nil)
			if tt.seed {
				s.Put("roles", seededRole())
			}
			desired := desiredRole()
			tt.edit(desired)
			var got map[string]any
			called := false
			found, diags := e.readInto(context.Background(), engRoleOID, desired, tt.afterWrite, func(actual map[string]any) {
				called, got = true, actual
			})
			noErrors(t, diags)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if called != tt.wantCallback {
				t.Fatalf("callback called = %v, want %v", called, tt.wantCallback)
			}
			if called {
				if got["description"] != "d" {
					t.Errorf("callback actual description = %v, want the server's d", got["description"])
				}
				for _, k := range []string{"version", "@metadata", "archetypeRef"} {
					if _, ok := got[k]; ok {
						t.Errorf("callback actual is not canonical: has %s", k)
					}
				}
			}
			var warnings []string
			for _, d := range diags.Warnings() {
				warnings = append(warnings, d.Detail())
			}
			switch {
			case tt.wantWarning == "" && len(warnings) != 0:
				t.Fatalf("unexpected warnings %v", warnings)
			case tt.wantWarning != "" && (len(warnings) != 1 || warnings[0] != tt.wantWarning):
				t.Fatalf("warnings = %v, want [%q]", warnings, tt.wantWarning)
			}
		})
	}
}

func TestEffectiveOptions_precedence(t *testing.T) {
	builtin := []string{"isImport", "raw"}
	tests := []struct {
		name     string
		defaults map[string][]string
		resource []string
		want     []string
	}{
		{name: "builtin when nothing configured", want: []string{"isImport", "raw"}},
		{name: "builtin when defaults name other resources", defaults: map[string][]string{"org": {"isImport"}}, want: []string{"isImport", "raw"}},
		{name: "provider default", defaults: map[string][]string{"role": {"isImport"}}, want: []string{"isImport"}},
		{name: "empty provider default sends none", defaults: map[string][]string{"role": {}}, want: []string{}},
		{name: "resource attribute", defaults: map[string][]string{"role": {"isImport"}}, resource: []string{"raw"}, want: []string{"raw"}},
		{name: "empty resource attribute sends none", defaults: map[string][]string{"role": {"isImport"}}, resource: []string{}, want: []string{}},
		{name: "overwrite only when listed", resource: []string{"overwrite"}, want: []string{"overwrite"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveOptions(tt.defaults, "role", tt.resource, builtin)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("effectiveOptions = %#v, want %#v", got, tt.want)
			}
			// The result never aliases its inputs: callers append to it.
			for i := range got {
				got[i] = "mutated"
			}
			if slices.Contains(builtin, "mutated") || slices.Contains(tt.resource, "mutated") || slices.Contains(tt.defaults["role"], "mutated") {
				t.Fatal("effectiveOptions result aliases an input slice")
			}
		})
	}
}

func TestPruneEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{name: "nothing to prune", in: map[string]any{"name": "r", "n": 0, "b": false, "s": ""}, want: map[string]any{"name": "r", "n": 0, "b": false, "s": ""}},
		{name: "empty list and map", in: map[string]any{"name": "r", "inducement": []any{}, "extension": map[string]any{}}, want: map[string]any{"name": "r"}},
		{name: "nil value", in: map[string]any{"name": "r", "description": nil}, want: map[string]any{"name": "r"}},
		{name: "map empty after pruning", in: map[string]any{"activation": map[string]any{"validFrom": nil, "x": []any{}}}, want: map[string]any{}},
		{name: "list elements empty after pruning", in: map[string]any{"assignment": []any{map[string]any{}, map[string]any{"targetRef": map[string]any{"oid": "x"}}}}, want: map[string]any{"assignment": []any{map[string]any{"targetRef": map[string]any{"oid": "x"}}}}},
		{name: "deep", in: map[string]any{"a": []any{map[string]any{"b": []any{map[string]any{"c": map[string]any{}}}}}}, want: map[string]any{}},
		{name: "nil input", in: nil, want: map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var before any
			if tt.in != nil {
				before = asJSON(t, tt.in)
			}
			got := pruneEmpty(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("pruneEmpty = %#v, want %#v", got, tt.want)
			}
			if tt.in != nil && !reflect.DeepEqual(asJSON(t, tt.in), before) {
				t.Fatal("pruneEmpty modified its input")
			}
		})
	}
}

// midPoint assigns a task the archetype of its activity when it runs: with
// no assignment configured, that is neither a difference nor a delta.
func TestEngine_taskArchetypeAssignmentIsNotADifference(t *testing.T) {
	eng, s := testEngine(t, engineSpec{RestType: "tasks", ShortName: "task", CreateOptions: []string{"isImport"}}, nil)
	const oid = "07000000-0000-4000-8000-0000000000aa"
	s.Put("tasks", map[string]any{
		"oid": oid, "name": "t", "executionState": "suspended",
		"assignment": []any{archetypeAssignment("00000000-0000-0000-0000-000000000502")},
	})
	desired := map[string]any{"name": "t"}
	found, diags := eng.readInto(context.Background(), oid, desired, false, func(map[string]any) {
		t.Error("readInto reported a difference")
	})
	noErrors(t, diags)
	if !found {
		t.Fatal("task not found")
	}
	noErrors(t, eng.update(context.Background(), oid, desired, nil))
	if p := requests(s, "PATCH"); len(p) != 0 {
		t.Fatalf("update sent %d PATCH requests, want none", len(p))
	}

	// A real change sends only that change, never a delete of the
	// assignment.
	noErrors(t, eng.update(context.Background(), oid, map[string]any{"name": "t", "description": "d"}, nil))
	p := requests(s, "PATCH")
	if len(p) != 1 {
		t.Fatalf("want one PATCH, got %d", len(p))
	}
	want := asJSON(t, map[string]any{"objectModification": map[string]any{"itemDelta": []any{
		map[string]any{"modificationType": "add", "path": "description", "value": "d"},
	}}})
	if got := asJSON(t, p[0].Body); !reflect.DeepEqual(got, want) {
		t.Errorf("PATCH body = %v, want %v", got, want)
	}
}

// TestEngineUpdate_differenceWithoutDeltaWarns: an empty list midPoint
// returns for an item the configuration leaves out is a difference (Equal)
// that no item delta expresses (Compute sends nothing for an absent item).
// The update sends nothing and says so, naming the item.
func TestEngineUpdate_differenceWithoutDeltaWarns(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	seeded := seededRole()
	seeded["subtype"] = []any{}
	s.Put("roles", seeded)
	diags := e.update(context.Background(), engRoleOID, desiredRole(), nil)
	noErrors(t, diags)
	if p := requests(s, http.MethodPatch); len(p) != 0 {
		t.Fatalf("sent %d PATCHes: %+v", len(p), p)
	}
	if diags.WarningsCount() != 1 {
		t.Fatalf("want one warning, got:\n%s", diagText(diags))
	}
	w := diags.Warnings()[0]
	want := "roles/" + engRoleOID + ": the configuration differs from midPoint in subtype but no item delta can express the change"
	if w.Summary() != "Change not sent to midPoint" || !strings.Contains(w.Detail(), want) {
		t.Errorf("warning = %q: %q, want detail containing %q", w.Summary(), w.Detail(), want)
	}

	// Equal bodies: no warning.
	s.Put("roles", seededRole())
	diags = e.update(context.Background(), engRoleOID, desiredRole(), nil)
	noErrors(t, diags)
	if diags.WarningsCount() != 0 {
		t.Errorf("unexpected warnings:\n%s", diagText(diags))
	}
}

// TestEngine_structuredServerFilter: a reference midPoint stores with a
// structured filter whose text rendering is the configured text filter is
// equal to the configuration; changing another item sends only that item,
// never a rewrite of the filter or of the container holding it.
func TestEngine_structuredServerFilter(t *testing.T) {
	e, s := testEngine(t, testRoleSpec, nil)
	seeded := seededRole()
	seeded["inducement"] = []any{map[string]any{"@id": json.Number("1"), "targetRef": map[string]any{
		"oid": engOtherRoleOID, "type": "c:RoleType", "relation": "org:default",
		"filter": map[string]any{"equal": map[string]any{"path": "name", "value": "grafana / viewer"}},
	}}}
	s.Put("roles", seeded)
	desired := desiredRole()
	desired["inducement"] = []any{map[string]any{"targetRef": map[string]any{
		"type": "c:RoleType", "filter": map[string]any{"text": `name = "grafana / viewer"`},
	}}}

	actual, _, diags := e.read(context.Background(), engRoleOID)
	noErrors(t, diags)
	equal, deltas, err := e.reconcile(context.Background(), desired, actual)
	if err != nil || !equal || len(deltas) != 0 {
		t.Fatalf("reconcile = %v, %v, %v; want equal with no deltas", equal, deltas, err)
	}

	desired["description"] = "new"
	noErrors(t, e.update(context.Background(), engRoleOID, desired, nil))
	patches := requests(s, http.MethodPatch)
	if len(patches) != 1 {
		t.Fatalf("sent %d PATCHes, want 1", len(patches))
	}
	if got := patches[0].Query["options"]; !reflect.DeepEqual(got, []string{"raw"}) {
		t.Errorf("update options = %v, want [raw] (no reevaluateSearchFilters)", got)
	}
	want := map[string]any{"objectModification": map[string]any{"itemDelta": []any{
		map[string]any{"modificationType": "replace", "path": "description", "value": "new"},
	}}}
	if got := asJSON(t, patches[0].Body); !reflect.DeepEqual(got, asJSON(t, want)) {
		t.Fatalf("PATCH body = %v\nwant %v", got, want)
	}
}
