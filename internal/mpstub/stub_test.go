package mpstub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

const (
	archetypeOID = "00000000-0000-0000-0000-000000000329"
	roleOID      = "11111111-1111-1111-1111-111111111111"
	otherRoleOID = "22222222-2222-2222-2222-222222222222"
	resourceOID  = "33333333-3333-3333-3333-333333333333"
)

func newStub(t *testing.T) (*Server, *client.Client) {
	t.Helper()
	s := New()
	t.Cleanup(s.Close)
	c, err := client.New(client.Config{URL: s.URL(), Username: "administrator", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

// jsonValue round-trips v through JSON so that Go literals and decoded
// bodies (json.Number) compare alike.
func jsonValue(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	g, w := jsonValue(t, got), jsonValue(t, want)
	if !reflect.DeepEqual(g, w) {
		gb, _ := json.MarshalIndent(g, "", "  ")
		wb, _ := json.MarshalIndent(w, "", "  ")
		t.Fatalf("got:\n%s\nwant:\n%s", gb, wb)
	}
}

func TestStubPut_assignsOIDAndContainerIDs(t *testing.T) {
	s, _ := newStub(t)
	oid := s.Put("roles", map[string]any{
		"name": "r1",
		"assignment": []any{
			map[string]any{"targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
		},
		"inducement": map[string]any{"targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
	})
	if oid == "" {
		t.Fatal("Put returned no oid")
	}
	got := s.Get("roles", oid)
	if got["oid"] != oid {
		t.Fatalf("stored oid = %v, want %s", got["oid"], oid)
	}
	assertJSONEqual(t, got, map[string]any{
		"oid":  oid,
		"name": "r1",
		"assignment": []any{
			map[string]any{"@id": 1, "targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
		},
		"inducement": map[string]any{"@id": 2, "targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
	})

	// An explicit oid is kept; Get of an unknown object is nil.
	if got := s.Put("roles", map[string]any{"oid": roleOID, "name": "r2"}); got != roleOID {
		t.Fatalf("Put kept oid %q, want %s", got, roleOID)
	}
	if s.Get("roles", "missing") != nil {
		t.Fatal("Get of a missing object is not nil")
	}
}

func TestStubGet_stampsServerNoise(t *testing.T) {
	s, c := newStub(t)
	s.Put("roles", map[string]any{"oid": otherRoleOID, "name": "other"})
	stored := map[string]any{
		"oid":  roleOID,
		"name": "r1",
		"assignment": []any{
			map[string]any{"targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
		},
		"inducement": []any{
			map[string]any{"targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
		},
	}
	s.Put("roles", stored)

	obj, err := c.Get(context.Background(), "roles", roleOID, client.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b := obj.Body
	for _, k := range []string{"@metadata", "version", "iteration", "iterationToken", "archetypeRef", "roleMembershipRef"} {
		if _, ok := b[k]; !ok {
			t.Errorf("GET body lacks %s", k)
		}
	}
	if act, _ := b["activation"].(map[string]any); act["effectiveStatus"] != "enabled" || act["enableTimestamp"] == nil {
		t.Errorf("activation = %v, want effectiveStatus enabled and enableTimestamp", b["activation"])
	}
	if ref, _ := b["archetypeRef"].(map[string]any); ref["oid"] != archetypeOID || ref["type"] != "c:ArchetypeType" {
		t.Errorf("archetypeRef = %v", b["archetypeRef"])
	}
	a := b["assignment"].([]any)[0].(map[string]any)
	for _, k := range []string{"@id", "@ns", "@metadata"} {
		if _, ok := a[k]; !ok {
			t.Errorf("assignment value lacks %s", k)
		}
	}
	if md, _ := a["@metadata"].(map[string]any); md["@id"] == nil {
		t.Errorf("assignment @metadata lacks @id: %v", a["@metadata"])
	}
	if ref := a["targetRef"].(map[string]any); ref["type"] != "c:ArchetypeType" || ref["relation"] != "org:default" {
		t.Errorf("assignment targetRef = %v, want c: type and default relation", ref)
	}
	ind := b["inducement"].([]any)[0].(map[string]any)
	if ref := ind["targetRef"].(map[string]any); ref["targetName"] != "other" {
		t.Errorf("inducement targetRef = %v, want resolved targetName", ref)
	}

	// All the noise is server-managed: canonically, GET equals what was stored.
	if want := normalize.Canonical("roles", s.Get("roles", roleOID)); !normalize.Equal(normalize.Canonical("roles", b), want) {
		t.Fatalf("canonical GET differs from canonical stored body:\n%v\n%v", normalize.Canonical("roles", b), want)
	}
	// The stored body is not changed by a GET.
	if _, ok := s.Get("roles", roleOID)["version"]; ok {
		t.Fatal("GET stamped noise into the stored body")
	}
}

func TestStubGet_notFound(t *testing.T) {
	_, c := newStub(t)
	_, err := c.Get(context.Background(), "roles", roleOID, client.GetOptions{})
	if !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "not found") {
		t.Fatalf("err = %#v, want a not-found message", err)
	}
}

func TestStubPatch(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"oid":         roleOID,
			"name":        "r1",
			"description": "d",
			"assignment": []any{
				map[string]any{"targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
			},
			"inducement": []any{
				map[string]any{"targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
				map[string]any{"description": "second", "targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
			},
		}
	}
	tests := []struct {
		name   string
		deltas []client.ItemDelta
		// want maps root items to their expected stored value; nil means absent.
		want    map[string]any
		wantErr bool
	}{
		{
			name:   "replace root property",
			deltas: []client.ItemDelta{{ModificationType: "replace", Path: "description", Value: "new"}},
			want:   map[string]any{"description": "new"},
		},
		{
			name:   "replace without value removes the item",
			deltas: []client.ItemDelta{{ModificationType: "replace", Path: "description"}},
			want:   map[string]any{"description": nil},
		},
		{
			name:   "add missing root item",
			deltas: []client.ItemDelta{{ModificationType: "add", Path: "riskLevel", Value: "high"}},
			want:   map[string]any{"riskLevel": "high"},
		},
		{
			name: "add container value gets a fresh id",
			deltas: []client.ItemDelta{{ModificationType: "add", Path: "assignment", Value: []any{
				map[string]any{"targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
			}}},
			want: map[string]any{"assignment": []any{
				map[string]any{"@id": 1, "targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
				map[string]any{"@id": 4, "targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
			}},
		},
		{
			name:   "delete container value by id",
			deltas: []client.ItemDelta{{ModificationType: "delete", Path: "inducement", Value: []any{map[string]any{"@id": 2}}}},
			want: map[string]any{"inducement": []any{
				map[string]any{"@id": 3, "description": "second", "targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
			}},
		},
		{
			name: "delete value by content",
			deltas: []client.ItemDelta{{ModificationType: "delete", Path: "inducement", Value: []any{
				map[string]any{"targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
			}}},
			want: map[string]any{"inducement": []any{
				map[string]any{"@id": 3, "description": "second", "targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
			}},
		},
		{
			name:   "delete last value removes the item",
			deltas: []client.ItemDelta{{ModificationType: "delete", Path: "assignment", Value: []any{map[string]any{"@id": 1}}}},
			want:   map[string]any{"assignment": nil},
		},
		{
			name:   "replace inside a container value by id",
			deltas: []client.ItemDelta{{ModificationType: "replace", Path: "inducement[3]/description", Value: "changed"}},
			want: map[string]any{"inducement": []any{
				map[string]any{"@id": 2, "targetRef": map[string]any{"oid": otherRoleOID, "type": "RoleType"}},
				map[string]any{"@id": 3, "description": "changed", "targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
			}},
		},
		{
			name:   "add nested below a container value creates the path",
			deltas: []client.ItemDelta{{ModificationType: "add", Path: "assignment[1]/activation/administrativeStatus", Value: "disabled"}},
			want: map[string]any{"assignment": []any{
				map[string]any{
					"@id":        1,
					"targetRef":  map[string]any{"oid": archetypeOID, "type": "ArchetypeType"},
					"activation": map[string]any{"administrativeStatus": "disabled"},
				},
			}},
		},
		{
			name: "several deltas apply in order",
			deltas: []client.ItemDelta{
				{ModificationType: "delete", Path: "inducement", Value: []any{map[string]any{"@id": 2}}},
				{ModificationType: "replace", Path: "description", Value: "x"},
			},
			want: map[string]any{
				"description": "x",
				"inducement": []any{
					map[string]any{"@id": 3, "description": "second", "targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}},
				},
			},
		},
		{
			name: "unknown container id fails and changes nothing",
			deltas: []client.ItemDelta{
				{ModificationType: "replace", Path: "description", Value: "x"},
				{ModificationType: "replace", Path: "inducement[99]/description", Value: "x"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, c := newStub(t)
			s.Put("roles", base())
			before := s.Get("roles", roleOID)
			err := c.Modify(context.Background(), "roles", roleOID, tt.deltas, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Modify succeeded, want an error")
				}
				assertJSONEqual(t, s.Get("roles", roleOID), before)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := before
			for k, v := range tt.want {
				if v == nil {
					delete(want, k)
				} else {
					want[k] = v
				}
			}
			assertJSONEqual(t, s.Get("roles", roleOID), want)
		})
	}
}

func TestStubPatch_bumpsVersionAndMissingObjectIs404(t *testing.T) {
	s, c := newStub(t)
	s.Put("roles", map[string]any{"oid": roleOID, "name": "r1"})
	ctx := context.Background()
	version := func() any {
		obj, err := c.Get(ctx, "roles", roleOID, client.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return obj.Body["version"]
	}
	v1 := version()
	if err := c.Modify(ctx, "roles", roleOID, []client.ItemDelta{{ModificationType: "replace", Path: "description", Value: "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if v2 := version(); v2 == v1 {
		t.Fatalf("version did not change: %v", v2)
	}
	err := c.Modify(ctx, "roles", otherRoleOID, []client.ItemDelta{{ModificationType: "replace", Path: "description", Value: "x"}}, nil)
	if !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStubCreate(t *testing.T) {
	tests := []struct {
		name       string
		restType   string
		seed       bool
		options    string
		body       string
		wantStatus int
	}{
		{name: "new object", restType: "roles", body: `{"role":{"oid":"` + roleOID + `","name":"r1"}}`, wantStatus: http.StatusCreated},
		{name: "generated oid", restType: "roles", body: `{"role":{"name":"r1"}}`, wantStatus: http.StatusCreated},
		{name: "task is accepted", restType: "tasks", body: `{"task":{"oid":"` + roleOID + `","name":"t1"}}`, wantStatus: http.StatusAccepted},
		{name: "existing oid conflicts", restType: "roles", seed: true, options: "?options=isImport&options=raw", body: `{"role":{"oid":"` + roleOID + `","name":"r1"}}`, wantStatus: http.StatusConflict},
		{name: "overwrite replaces", restType: "roles", seed: true, options: "?options=overwrite", body: `{"role":{"oid":"` + roleOID + `","name":"r1"}}`, wantStatus: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newStub(t)
			if tt.seed {
				s.Put(tt.restType, map[string]any{"oid": roleOID, "name": "seeded"})
			}
			resp, err := http.Post(s.URL()+"/midpoint/ws/rest/"+tt.restType+tt.options, "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusConflict {
				var res map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res["message"] == nil {
					t.Fatalf("conflict body = %v (%v), want a message", res, err)
				}
				if got := s.Get(tt.restType, roleOID)["name"]; got != "seeded" {
					t.Fatalf("conflict changed the object: name = %v", got)
				}
				return
			}
			loc := resp.Header.Get("Location")
			prefix := s.URL() + "/midpoint/ws/rest/" + tt.restType + "/"
			if !strings.HasPrefix(loc, prefix) {
				t.Fatalf("Location = %q, want prefix %q", loc, prefix)
			}
			oid := strings.TrimPrefix(loc, prefix)
			if got := s.Get(tt.restType, oid); got == nil || got["name"] == "seeded" || got["oid"] != oid {
				t.Fatalf("stored = %v", got)
			}
		})
	}
}

func TestStubDelete(t *testing.T) {
	s, c := newStub(t)
	s.Put("roles", map[string]any{"oid": roleOID, "name": "r1"})
	ctx := context.Background()
	if err := c.Delete(ctx, "roles", roleOID, nil); err != nil {
		t.Fatal(err)
	}
	if s.Get("roles", roleOID) != nil {
		t.Fatal("object still stored after delete")
	}
	if err := c.Delete(ctx, "roles", roleOID, nil); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestStubSearch(t *testing.T) {
	membersFilter := `roleMembershipRef matches (oid = "` + roleOID + `") or assignment/targetRef matches (oid = "` + roleOID + `")`
	seed := func(s *Server) {
		s.Put("roles", map[string]any{"oid": roleOID, "name": "target"})
		s.Put("users", map[string]any{"name": "alice", "assignment": map[string]any{"targetRef": map[string]any{"oid": roleOID, "type": "RoleType"}}})
		s.Put("orgs", map[string]any{"name": map[string]any{"orig": "org1", "norm": "org1"}, "assignment": []any{map[string]any{"targetRef": map[string]any{"oid": roleOID, "type": "c:RoleType"}}}})
		s.Put("users", map[string]any{"name": "bob"})
		// roleMembershipRef derived from an archetype assignment.
		s.Put("services", map[string]any{"name": "svc", "assignment": map[string]any{"targetRef": map[string]any{"oid": archetypeOID, "type": "ArchetypeType"}}})
		s.Put("shadows", map[string]any{"name": "uid=a", "resourceRef": map[string]any{"oid": resourceOID, "type": "ResourceType"}})
		s.Put("shadows", map[string]any{"name": "uid=b", "resourceRef": map[string]any{"oid": otherRoleOID, "type": "ResourceType"}})
	}
	tests := []struct {
		name      string
		restType  string
		filter    string
		maxSize   int
		totals    bool
		wantNames []string
		wantTotal int
	}{
		{name: "members over all types", restType: "assignmentHolders", filter: membersFilter, wantNames: []string{"alice", "org1"}, wantTotal: -1},
		{name: "members via archetype roleMembershipRef", restType: "assignmentHolders",
			filter:    `roleMembershipRef matches (oid = "` + archetypeOID + `") or assignment/targetRef matches (oid = "` + archetypeOID + `")`,
			wantNames: []string{"svc"}, wantTotal: -1},
		{name: "maxSize limits results", restType: "assignmentHolders", filter: membersFilter, maxSize: 1, wantNames: []string{"alice"}, wantTotal: -1},
		{name: "totals reported when enabled", restType: "assignmentHolders", filter: membersFilter, maxSize: 1, totals: true, wantNames: []string{"alice"}, wantTotal: 2},
		{name: "shadows of a resource", restType: "shadows", filter: `resourceRef matches (oid = "` + resourceOID + `")`, wantNames: []string{"uid=a"}, wantTotal: -1},
		{name: "no matches", restType: "shadows", filter: `resourceRef matches (oid = "` + roleOID + `")`, wantTotal: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, c := newStub(t)
			seed(s)
			s.ReportTotals(tt.totals)
			objs, total, err := c.Search(context.Background(), tt.restType, tt.filter, tt.maxSize, nil)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, o := range objs {
				switch n := o.Body["name"].(type) {
				case string:
					names = append(names, n)
				case map[string]any:
					names = append(names, n["orig"].(string))
				}
				if o.OID == "" {
					t.Errorf("result without oid: %v", o.Body)
				}
			}
			if !reflect.DeepEqual(names, tt.wantNames) {
				t.Errorf("names = %v, want %v", names, tt.wantNames)
			}
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
		})
	}
}

func TestStubSearch_unsupportedFilter(t *testing.T) {
	_, c := newStub(t)
	_, _, err := c.Search(context.Background(), "roles", `name = "x"`, 0, nil)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want HTTP 400", err)
	}
}

func TestStubServerVersion(t *testing.T) {
	_, c := newStub(t)
	v, err := c.ServerVersion(context.Background())
	if err != nil || v != "4.9.9" {
		t.Fatalf("ServerVersion = %q, %v", v, err)
	}
}

func TestStubRequests_recordsMethodPathQueryBody(t *testing.T) {
	s, c := newStub(t)
	s.Put("roles", map[string]any{"oid": roleOID, "name": "r1"})
	ctx := context.Background()
	if _, err := c.Get(ctx, "roles", roleOID, client.GetOptions{Options: []string{"noFetch"}, Exclude: []string{"metadata"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Modify(ctx, "roles", roleOID, []client.ItemDelta{{ModificationType: "replace", Path: "description", Value: "x"}}, []string{"raw"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, client.Object{Type: "roles", Body: map[string]any{"name": "r2", "assignment": []any{map[string]any{"description": "a"}}}}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := s.Requests()
	if len(reqs) != 3 {
		t.Fatalf("recorded %d requests, want 3 (Put is not a request): %+v", len(reqs), reqs)
	}
	get, patch, post := reqs[0], reqs[1], reqs[2]
	// The record is what was sent, not the stored object with its generated
	// oid and container ids.
	assertJSONEqual(t, post.Body, map[string]any{"role": map[string]any{"name": "r2", "assignment": []any{map[string]any{"description": "a"}}}})
	if get.Method != http.MethodGet || get.Path != "/roles/"+roleOID || get.Query.Get("options") != "noFetch" || get.Query.Get("exclude") != "metadata" || get.Body != nil {
		t.Errorf("GET recorded as %+v", get)
	}
	if patch.Method != http.MethodPatch || patch.Path != "/roles/"+roleOID || patch.Query.Get("options") != "raw" {
		t.Errorf("PATCH recorded as %+v", patch)
	}
	assertJSONEqual(t, patch.Body, map[string]any{"objectModification": map[string]any{"itemDelta": []any{
		map[string]any{"modificationType": "replace", "path": "description", "value": "x"},
	}}})
}

func TestStubSetFault(t *testing.T) {
	s, c := newStub(t)
	s.Put("roles", map[string]any{"oid": roleOID, "name": "r1"})
	ctx := context.Background()
	failed := 0
	s.SetFault(func(r Recorded) int {
		if r.Method == http.MethodGet && failed == 0 {
			failed++
			return http.StatusInternalServerError
		}
		return 0
	})
	_, err := c.Get(ctx, "roles", roleOID, client.GetOptions{})
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("faulted GET: err = %v, want an HTTP 500 APIError", err)
	}
	if _, err := c.Get(ctx, "roles", roleOID, client.GetOptions{}); err != nil {
		t.Fatalf("GET after the fault: %v", err)
	}
	if n := len(s.Requests()); n != 2 {
		t.Errorf("faulted requests are recorded too: got %d records, want 2", n)
	}
	s.SetFault(nil)
	if _, err := c.Get(ctx, "roles", roleOID, client.GetOptions{}); err != nil {
		t.Fatalf("GET after clearing the fault: %v", err)
	}
}
