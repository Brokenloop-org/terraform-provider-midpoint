package convert

import (
	"reflect"
	"strings"
	"testing"
)

// catalogue loads, projects and dedupes the fixture.
func catalogue(t *testing.T) []Resource {
	t.Helper()
	srcs, _, err := Load([]string{"testdata/catalogue"})
	if err != nil {
		t.Fatal(err)
	}
	var rs []Resource
	for _, s := range srcs {
		r, err := Project(s)
		if err != nil {
			t.Fatal(err)
		}
		rs = append(rs, r)
	}
	DedupeLabels(rs)
	return rs
}

func byOID(t *testing.T, rs []Resource, oid string) Resource {
	t.Helper()
	for _, r := range rs {
		if r.OID == oid {
			return r
		}
	}
	t.Fatalf("no resource %s", oid)
	return Resource{}
}

func targets(r Resource) []Block {
	var out []Block
	for _, b := range r.Blocks {
		out = append(out, blocksOf(Resource{Blocks: b.Blocks}, "target")...)
	}
	return out
}

const (
	vaultOID   = "3f6a1c52-8d0e-4b7a-9a41-5c2e7d9b0a11"
	archOID    = "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02"
	opRoleOID  = "9a5bdeed-fa50-4a7d-bebc-604e5ddf0298"
	obsRoleOID = "e7b30a96-4d85-4c21-b1f7-92a05d6c8e44"
	grafRole   = "c41d7e03-6b52-4f19-a8d4-0e9b3a7f5c33"
	platOrgOID = "6d0e3b81-9c47-4a52-b3e8-5a17f2c04977"
	acmeOID    = "1a9c5d70-3e84-4b26-9f0d-8c71b4a2e655"
	taskOID    = "214ed436-8d6e-4a57-b63b-f87bb8305a34"
	resOID     = "25e166d6-9899-43b7-b0d7-4c4c526b7a9e"
)

func TestRewrite(t *testing.T) {
	t.Run("service from role", func(t *testing.T) {
		rs := catalogue(t)
		out, _, _ := Rewrite(rs)
		role := byOID(t, out, opRoleOID)
		vault := byOID(t, rs, vaultOID)
		var found bool
		for _, b := range role.Blocks {
			for _, tb := range b.Blocks {
				if tb.Type == "target" {
					want := Ref{Address: "midpoint_service." + vault.Label + ".oid"}
					if tb.Attrs["oid"] == want {
						found = true
					}
				}
			}
		}
		if !found {
			t.Errorf("no target with oid %v in %+v", vault.Label, role.Blocks)
		}
		// the input is untouched
		if got := targets(byOID(t, rs, opRoleOID))[0].Attrs["oid"]; got != vaultOID {
			t.Errorf("input modified: %v", got)
		}
		// archetype shortcut to a converted archetype
		svc := byOID(t, out, vaultOID)
		arch := byOID(t, rs, archOID)
		if got, want := svc.Attrs["archetype"], (Ref{Address: "midpoint_archetype." + arch.Label + ".oid"}); got != want {
			t.Errorf("archetype = %#v, want %#v", got, want)
		}
	})

	t.Run("builtin not converted becomes data", func(t *testing.T) {
		rs := catalogue(t)
		var kept []Resource
		for _, r := range rs {
			if r.OID != "00000000-0000-0000-0000-000000000328" {
				kept = append(kept, r)
			}
		}
		out, data, notes := Rewrite(kept)
		want := []DataLookup{{Label: "archetype_328", RestType: "archetypes", OID: "00000000-0000-0000-0000-000000000328"}}
		// the task owner (user 2) is a built-in as well
		want = append(want, DataLookup{Label: "user_2", RestType: "users", OID: "00000000-0000-0000-0000-000000000002"})
		if !reflect.DeepEqual(sortLookups(data), sortLookups(want)) {
			t.Errorf("data = %+v, want %+v", data, want)
		}
		task := byOID(t, out, taskOID)
		if task.Attrs["owner"] != (Ref{Address: "data.midpoint_object.user_2.oid"}) {
			t.Errorf("owner = %#v", task.Attrs["owner"])
		}
		for _, n := range notes {
			if strings.Contains(n.Message, "00000000-0000-0000-0000") {
				t.Errorf("unexpected note for a built-in: %v", n)
			}
		}
	})

	t.Run("unknown oid stays literal", func(t *testing.T) {
		const unknown = "ffffffff-0000-4000-8000-000000000001"
		in := []Resource{{Kind: "midpoint_role", Label: "a", OID: "aaaaaaaa-0000-4000-8000-000000000001", Attrs: map[string]any{},
			Blocks: []Block{{Type: "inducement", Blocks: []Block{{Type: "target", Attrs: map[string]any{"oid": unknown, "type": "c:RoleType"}}}}}}}
		out, data, notes := Rewrite(in)
		if got := out[0].Blocks[0].Blocks[0].Attrs["oid"]; got != unknown {
			t.Errorf("oid = %#v", got)
		}
		if len(data) != 0 || len(notes) != 1 || !strings.Contains(notes[0].Message, unknown) {
			t.Errorf("data %v notes %v", data, notes)
		}
	})

	t.Run("oid and filter", func(t *testing.T) {
		rs := catalogue(t)
		// converted oid: Ref and filter both kept
		out, _, _ := Rewrite(rs)
		tb := targets(byOID(t, out, obsRoleOID))
		var withFilter Block
		for _, b := range tb {
			if _, ok := b.Attrs["filter"]; ok {
				withFilter = b
			}
		}
		graf := byOID(t, rs, grafRole)
		if withFilter.Attrs["oid"] != (Ref{Address: "midpoint_role." + graf.Label + ".oid"}) || withFilter.Attrs["filter"] == "" {
			t.Errorf("converted: %+v", withFilter.Attrs)
		}
		// unconverted oid: filter only
		var kept []Resource
		for _, r := range rs {
			if r.OID != grafRole {
				kept = append(kept, r)
			}
		}
		out, _, notes := Rewrite(kept)
		var filtered int
		for _, b := range targets(byOID(t, out, obsRoleOID)) {
			if f, ok := b.Attrs["filter"]; ok {
				filtered++
				if _, has := b.Attrs["oid"]; has || f != `name = "grafana / viewer"` {
					t.Errorf("want filter only: %+v", b.Attrs)
				}
			}
		}
		if filtered != 1 {
			t.Errorf("filtered targets = %d, want 1", filtered)
		}
		if len(notes) == 0 {
			t.Error("no note for the dropped oid")
		}
	})

	t.Run("type mismatch warns", func(t *testing.T) {
		in := []Resource{
			{Kind: "midpoint_service", Label: "s", OID: "aaaaaaaa-0000-4000-8000-000000000001", Attrs: map[string]any{}},
			{Kind: "midpoint_role", Label: "r", OID: "aaaaaaaa-0000-4000-8000-000000000002", Attrs: map[string]any{},
				Blocks: []Block{{Type: "inducement", Blocks: []Block{{Type: "target", Attrs: map[string]any{
					"oid": "aaaaaaaa-0000-4000-8000-000000000001", "type": "c:RoleType"}}}}}},
		}
		out, _, notes := Rewrite(in)
		if got := out[1].Blocks[0].Blocks[0].Attrs["oid"]; got != (Ref{Address: "midpoint_service.s.oid"}) {
			t.Errorf("oid = %#v", got)
		}
		if len(notes) != 1 || !strings.Contains(notes[0].Message, "midpoint_service") || !strings.Contains(notes[0].Message, "RoleType") {
			t.Errorf("notes = %v", notes)
		}
	})

	t.Run("no self reference cycle", func(t *testing.T) {
		in := []Resource{{Kind: "midpoint_org", Label: "o", OID: acmeOID, Attrs: map[string]any{},
			Blocks: []Block{{Type: "assignment", Blocks: []Block{{Type: "target", Attrs: map[string]any{"oid": acmeOID, "type": "c:OrgType"}}}}}}}
		out, _, _ := Rewrite(in)
		if got := out[0].Blocks[0].Blocks[0].Attrs["oid"]; got != acmeOID {
			t.Errorf("oid = %#v", got)
		}
		// the fixture's platform org references its parent, not itself
		rs := catalogue(t)
		o, _, _ := Rewrite(rs)
		acme := byOID(t, rs, acmeOID)
		if got := targets(byOID(t, o, platOrgOID))[0].Attrs["oid"]; got != (Ref{Address: "midpoint_org." + acme.Label + ".oid"}) {
			t.Errorf("parent = %#v", got)
		}
	})

	t.Run("task resource ref and free-form maps", func(t *testing.T) {
		rs := catalogue(t)
		out, _, _ := Rewrite(rs)
		res := byOID(t, rs, resOID)
		task := byOID(t, out, taskOID)
		want := Ref{Address: "midpoint_resource." + res.Label + ".oid"}
		var found bool
		var walk func(v any)
		walk = func(v any) {
			switch t := v.(type) {
			case map[string]any:
				if t["oid"] == want {
					found = true
				}
				for _, e := range t {
					walk(e)
				}
			case []any:
				for _, e := range t {
					walk(e)
				}
			}
		}
		walk(task.Attrs)
		walk(task.Extra)
		if !found {
			t.Errorf("no resourceRef rewritten in %v / %v", task.Attrs, task.Extra)
		}
	})

	t.Run("construction resource and nested map", func(t *testing.T) {
		in := []Resource{
			{Kind: "midpoint_resource", Label: "res", OID: "aaaaaaaa-0000-4000-8000-000000000001", Attrs: map[string]any{}},
			{Kind: "midpoint_role", Label: "r", OID: "aaaaaaaa-0000-4000-8000-000000000002", Attrs: map[string]any{},
				Blocks: []Block{{Type: "inducement", Blocks: []Block{{Type: "construction", Attrs: map[string]any{
					"resource": "aaaaaaaa-0000-4000-8000-000000000001",
					"body":     map[string]any{"association": []any{map[string]any{"targetRef": map[string]any{"oid": "aaaaaaaa-0000-4000-8000-000000000001"}}}},
				}}}}}},
		}
		out, _, _ := Rewrite(in)
		c := out[1].Blocks[0].Blocks[0]
		want := Ref{Address: "midpoint_resource.res.oid"}
		if c.Attrs["resource"] != want {
			t.Errorf("resource = %#v", c.Attrs["resource"])
		}
		inner := c.Attrs["body"].(map[string]any)["association"].([]any)[0].(map[string]any)["targetRef"].(map[string]any)
		if inner["oid"] != want {
			t.Errorf("nested = %#v", inner)
		}
	})
}

func sortLookups(d []DataLookup) []DataLookup {
	out := append([]DataLookup(nil), d...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Label < out[i].Label {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestRewriteContainers(t *testing.T) {
	const oid = "aaaaaaaa-0000-4000-8000-000000000001"
	target := Resource{Kind: "midpoint_resource", Label: "res", OID: oid, Attrs: map[string]any{}}
	inner := map[string]any{"oid": oid}
	list := []map[string]any{{"resourceRef": inner}}
	in := []Resource{target, {Kind: "midpoint_role", Label: "r", OID: "aaaaaaaa-0000-4000-8000-000000000002",
		Attrs: map[string]any{"x": map[string]any{"l": list}}}}
	out, _, _ := Rewrite(in)
	got := out[1].Attrs["x"].(map[string]any)["l"].([]any)[0].(map[string]any)["resourceRef"].(map[string]any)
	if got["oid"] != (Ref{Address: "midpoint_resource.res.oid"}) {
		t.Errorf("not rewritten: %#v", got)
	}
	if inner["oid"] != oid {
		t.Errorf("input mutated: %#v", inner)
	}
}

func TestRewriteGatesAndHints(t *testing.T) {
	t.Run("non oid owner and non task owner are not references", func(t *testing.T) {
		in := []Resource{
			{Kind: "midpoint_role", Label: "r", OID: "aaaaaaaa-0000-4000-8000-000000000002", Attrs: map[string]any{"owner": "00000000-0000-0000-0000-000000000002", "archetype": "not an oid"}},
			{Kind: "midpoint_task", Label: "t", OID: "aaaaaaaa-0000-4000-8000-000000000003", Attrs: map[string]any{"owner": "someone"}},
		}
		out, data, notes := Rewrite(in)
		if out[0].Attrs["owner"] != "00000000-0000-0000-0000-000000000002" || out[0].Attrs["archetype"] != "not an oid" || out[1].Attrs["owner"] != "someone" {
			t.Errorf("attrs changed: %v %v", out[0].Attrs, out[1].Attrs)
		}
		if len(data) != 0 || len(notes) != 0 {
			t.Errorf("data %v notes %v", data, notes)
		}
	})
	t.Run("typeless ref infers type from key", func(t *testing.T) {
		in := []Resource{{Kind: "midpoint_role", Label: "r", OID: "aaaaaaaa-0000-4000-8000-000000000002", Extra: map[string]any{
			"a": map[string]any{"resourceRef": map[string]any{"oid": "00000000-0000-0000-0000-000000000010"}},
			"b": map[string]any{"fooRef": map[string]any{"oid": "00000000-0000-0000-0000-000000000011"}},
		}}}
		_, data, _ := Rewrite(in)
		want := []DataLookup{{Label: "resource_10", RestType: "resources", OID: "00000000-0000-0000-0000-000000000010"},
			{Label: "object_11", RestType: "objects", OID: "00000000-0000-0000-0000-000000000011"}}
		if !reflect.DeepEqual(data, want) {
			t.Errorf("data = %+v", data)
		}
	})
}
