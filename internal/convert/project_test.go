package convert

import (
	"reflect"
	"testing"
)

// fixture projects the catalogue object with the given oid.
func fixture(t *testing.T, oid string) Resource {
	t.Helper()
	srcs, _, err := Load([]string{"testdata/catalogue"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if s.OID == oid {
			r, err := Project(s)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
	}
	t.Fatalf("no fixture with oid %s", oid)
	return Resource{}
}

func blocksOf(r Resource, typ string) []Block {
	var out []Block
	for _, b := range r.Blocks {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}

func expectAttrs(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("attrs\n got: %#v\nwant: %#v", got, want)
	}
}

func TestProject(t *testing.T) {
	t.Run("service minimal to archetype shortcut", func(t *testing.T) {
		r := fixture(t, "3f6a1c52-8d0e-4b7a-9a41-5c2e7d9b0a11")
		if r.Kind != "midpoint_service" || r.Label != "vault" || r.AdoptExisting {
			t.Errorf("kind/label/adopt = %s/%s/%v", r.Kind, r.Label, r.AdoptExisting)
		}
		expectAttrs(t, r.Attrs, map[string]any{
			"name": "Vault", "display_name": "Vault", "description": "Secrets management platform.",
			"identifier": "vault", "archetype": "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02",
		})
		if len(r.Blocks) != 0 || len(r.Extra) != 0 || len(r.Notes) != 0 {
			t.Errorf("blocks/extra/notes = %v/%v/%v", r.Blocks, r.Extra, r.Notes)
		}
	})

	t.Run("role filter inducement", func(t *testing.T) {
		r := fixture(t, "035d31a5-7403-4aff-86a8-0f941e5a6403")
		expectAttrs(t, r.Attrs, map[string]any{
			"name": "Security Reviewer", "display_name": "Security Reviewer",
			"description": "Provides access to security office tooling.", "risk_level": "high",
			"requestable": false, "archetype": "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02",
		})
		ind := blocksOf(r, "inducement")
		if len(ind) != 1 || len(ind[0].Blocks) != 1 {
			t.Fatalf("inducements = %#v", r.Blocks)
		}
		if ind[0].Blocks[0].Type != "target" {
			t.Fatalf("block = %s", ind[0].Blocks[0].Type)
		}
		expectAttrs(t, ind[0].Blocks[0].Attrs, map[string]any{"filter": `name = "vault / operator"`, "type": "RoleType"})
		if len(ind[0].Attrs) != 0 || len(r.Extra) != 0 {
			t.Errorf("inducement attrs/extra = %v/%v", ind[0].Attrs, r.Extra)
		}

		// oid and filter together.
		r = fixture(t, "e7b30a96-4d85-4c21-b1f7-92a05d6c8e44")
		ind = blocksOf(r, "inducement")
		if len(ind) != 1 {
			t.Fatalf("inducements = %#v", r.Blocks)
		}
		expectAttrs(t, ind[0].Blocks[0].Attrs, map[string]any{
			"oid": "c41d7e03-6b52-4f19-a8d4-0e9b3a7f5c33", "filter": `name = "grafana / viewer"`, "type": "RoleType",
		})
	})

	t.Run("role inducement by oid keeps no relation", func(t *testing.T) {
		r := fixture(t, "c41d7e03-6b52-4f19-a8d4-0e9b3a7f5c33")
		ind := blocksOf(r, "inducement")
		if len(ind) != 1 {
			t.Fatalf("inducements = %#v", r.Blocks)
		}
		expectAttrs(t, ind[0].Blocks[0].Attrs, map[string]any{"oid": "8c2e90b4-17d3-4a65-8f0b-d3a6e15c7b22", "type": "ServiceType"})
		// The built-in archetype stays an attribute.
		if r.Attrs["archetype"] != "00000000-0000-0000-0000-000000000328" {
			t.Errorf("archetype = %v", r.Attrs["archetype"])
		}
	})

	t.Run("tenant org", func(t *testing.T) {
		r := fixture(t, "1a9c5d70-3e84-4b26-9f0d-8c71b4a2e655")
		if r.Kind != "midpoint_org" || r.Label != "acme_corp" {
			t.Errorf("kind/label = %s/%s", r.Kind, r.Label)
		}
		expectAttrs(t, r.Attrs, map[string]any{
			"name": "Acme Corp", "display_name": "Acme Corp", "identifier": "acme", "tenant": true,
			"archetype": "d2f84b19-7a03-4e5c-8b66-1f0c9e3a7d66",
		})
	})

	t.Run("org assignments with parent and tenant", func(t *testing.T) {
		r := fixture(t, "6d0e3b81-9c47-4a52-b3e8-5a17f2c04977")
		as := blocksOf(r, "assignment")
		if len(as) != 2 {
			t.Fatalf("assignments = %#v", r.Blocks)
		}
		org := map[string]any{"oid": "1a9c5d70-3e84-4b26-9f0d-8c71b4a2e655", "type": "OrgType"}
		expectAttrs(t, as[0].Blocks[0].Attrs, org)
		if len(as[0].Attrs) != 0 {
			t.Errorf("first assignment attrs = %v", as[0].Attrs)
		}
		expectAttrs(t, as[1].Blocks[0].Attrs, org)
		// tenantRef is not a block item: it goes to the block extra, a
		// free-form map the renderer jsonencodes.
		expectAttrs(t, as[1].Attrs, map[string]any{"extra": map[string]any{"tenantRef": map[string]any{
			"oid": "1a9c5d70-3e84-4b26-9f0d-8c71b4a2e655", "relation": "org:default", "type": "OrgType",
		}}})
		if len(blocksOf(r, "inducement")) != 1 {
			t.Errorf("inducements = %#v", r.Blocks)
		}
	})

	t.Run("builtin adopt existing", func(t *testing.T) {
		r := fixture(t, "00000000-0000-0000-0000-000000000328")
		if !r.AdoptExisting || r.Kind != "midpoint_archetype" || r.OID != "00000000-0000-0000-0000-000000000328" {
			t.Errorf("adopt/kind/oid = %v/%s/%s", r.AdoptExisting, r.Kind, r.OID)
		}
		if r.Label != "application_role" {
			t.Errorf("label = %q", r.Label)
		}
		holder, _ := r.Attrs["holder_type"].([]any)
		if !reflect.DeepEqual(holder, []any{"RoleType"}) {
			t.Errorf("holder_type = %#v", r.Attrs["holder_type"])
		}
		if len(r.Blocks) != 0 || len(r.Extra) != 0 {
			t.Errorf("blocks/extra = %v/%v", r.Blocks, r.Extra)
		}
		if fixture(t, "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02").AdoptExisting {
			t.Error("custom archetype adopts")
		}
	})

	t.Run("archetype policy", func(t *testing.T) {
		r := fixture(t, "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02")
		want := map[string]any{"display": map[string]any{"label": "Platform item", "pluralLabel": "Platform items"}}
		if !reflect.DeepEqual(r.Attrs["archetype_policy"], want) {
			t.Errorf("archetype_policy = %#v", r.Attrs["archetype_policy"])
		}
	})

	t.Run("resource connector filter", func(t *testing.T) {
		r := fixture(t, "25e166d6-9899-43b7-b0d7-4c4c526b7a9e")
		if r.Kind != "midpoint_resource" || r.Label != "users_csv" {
			t.Errorf("kind/label = %s/%s", r.Kind, r.Label)
		}
		expectAttrs(t, r.Attrs["connector"].(map[string]any), map[string]any{
			"type": "com.evolveum.polygon.connector.csv.CsvConnector", "version": "2.8",
		})
		if r.Attrs["lifecycle_state"] != "active" || r.Attrs["name"] != "Users CSV" {
			t.Errorf("attrs = %v", r.Attrs)
		}
		cc := r.Attrs["connector_configuration"].(map[string]any)["configurationProperties"].(map[string]any)
		if cc["filePath"] != "/data/users.csv" {
			t.Errorf("connector_configuration = %v", r.Attrs["connector_configuration"])
		}
		if _, ok := r.Extra["connectorRef"]; ok || len(r.Extra) != 0 {
			t.Errorf("extra = %v", r.Extra)
		}
	})

	t.Run("resource connector text filter, schema and capabilities", func(t *testing.T) {
		s := Source{RestType: "resources", OID: "11111111-1111-4111-8111-111111111111", Body: map[string]any{
			"name": "ldap",
			"connectorRef": map[string]any{"type": "ConnectorType", "relation": "org:default", "oid": "9e2f0000-0000-4000-8000-000000000000",
				"filter": map[string]any{"text": `connectorType = "a.b.LdapConnector"`}},
			"capabilities": map[string]any{"configured": map[string]any{"create": map[string]any{"enabled": false}}},
			"schema": map[string]any{
				"generationConstraints": map[string]any{"generateObjectClass": []any{"ri:AccountObjectClass"}},
				"definition":            map[string]any{"@ns": "http://www.w3.org/2001/XMLSchema", "schema": "<xsd:schema/>"},
			},
		}}
		r, err := Project(s)
		if err != nil {
			t.Fatal(err)
		}
		expectAttrs(t, r.Attrs["connector"].(map[string]any), map[string]any{"type": "a.b.LdapConnector"})
		if !reflect.DeepEqual(r.Attrs["capabilities"], map[string]any{"create": map[string]any{"enabled": false}}) {
			t.Errorf("capabilities = %v", r.Attrs["capabilities"])
		}
		expectAttrs(t, r.Attrs["schema"].(map[string]any), map[string]any{
			"generation_constraints": map[string]any{"generateObjectClass": []any{"ri:AccountObjectClass"}},
			"definition":             "<xsd:schema/>",
		})
		if len(r.Extra) != 0 {
			t.Errorf("extra = %v", r.Extra)
		}
	})

	t.Run("resource connector not expressible stays in extra", func(t *testing.T) {
		s := Source{RestType: "resources", OID: "11111111-1111-4111-8111-111111111111", Body: map[string]any{
			"name":         "x",
			"connectorRef": map[string]any{"type": "ConnectorType", "oid": "9e2f0000-0000-4000-8000-000000000000"},
		}}
		r, _ := Project(s)
		if _, ok := r.Attrs["connector"]; ok || r.Extra["connectorRef"] == nil {
			t.Errorf("attrs/extra = %v/%v", r.Attrs, r.Extra)
		}
	})

	t.Run("task", func(t *testing.T) {
		r := fixture(t, "214ed436-8d6e-4a57-b63b-f87bb8305a34")
		if r.Kind != "midpoint_task" {
			t.Errorf("kind = %s", r.Kind)
		}
		wantAct := map[string]any{"work": map[string]any{"reconciliation": map[string]any{"resourceObjects": map[string]any{
			"intent": "default", "kind": "account",
			"resourceRef": map[string]any{"oid": "25e166d6-9899-43b7-b0d7-4c4c526b7a9e", "relation": "org:default", "type": "ResourceType"},
		}}}}
		expectAttrs(t, r.Attrs, map[string]any{
			"name": "Users CSV reconciliation", "binding": "loose", "owner": "00000000-0000-0000-0000-000000000002",
			"initial_execution_state": "suspended", "activity": wantAct,
		})
		if len(r.Extra) != 0 {
			t.Errorf("extra = %v", r.Extra)
		}
	})

	t.Run("task runnable and recurrence", func(t *testing.T) {
		r, _ := Project(Source{RestType: "tasks", Body: map[string]any{
			"name": "t", "executionState": "runnable", "schedulingState": "ready", "recurrence": "recurring",
			"schedule": map[string]any{"interval": 3600.0},
		}})
		expectAttrs(t, r.Attrs, map[string]any{
			"name": "t", "initial_execution_state": "runnable", "recurrence": "recurring",
			"schedule": map[string]any{"interval": 3600.0},
		})
	})

	t.Run("object template and mark", func(t *testing.T) {
		r := fixture(t, "a395aa95-3aea-4db3-893c-516cf8ca422b")
		if r.Kind != "midpoint_object_template" || r.Label != "person_object_template" {
			t.Errorf("kind/label = %s/%s", r.Kind, r.Label)
		}
		mapping, _ := r.Attrs["mapping"].([]any)
		if len(mapping) != 1 || r.Attrs["description"] != "Generates the full name of a person." {
			t.Errorf("attrs = %v", r.Attrs)
		}
		m := fixture(t, "26d8fa29-0eb4-412b-93bb-da9ecfbb8a1a")
		if m.Kind != "midpoint_mark" || m.Attrs["name"] != "Missing from CSV - review" || m.Label != "missing_from_csv_review" {
			t.Errorf("mark = %v %s", m.Attrs, m.Label)
		}
	})

	t.Run("unknown type generic", func(t *testing.T) {
		r := fixture(t, "7e2b8f10-5a36-4d94-a0c7-3b69d1e48f77")
		if r.Kind != "midpoint_object" || r.Attrs["type"] != "functionLibraries" || r.Label != "naming_functions" {
			t.Errorf("kind/type/label = %s/%v/%s", r.Kind, r.Attrs["type"], r.Label)
		}
		body := r.Attrs["body"].(map[string]any)
		if _, has := body["oid"]; has {
			t.Error("body holds oid")
		}
		// name is computed from body by the provider, so body keeps it.
		if body["name"] != "Naming functions" || body["function"] == nil {
			t.Errorf("body = %v", body)
		}
		if r.OID != "7e2b8f10-5a36-4d94-a0c7-3b69d1e48f77" || len(r.Extra) != 0 {
			t.Errorf("oid/extra = %s/%v", r.OID, r.Extra)
		}
	})

	t.Run("unclaimed goes to extra", func(t *testing.T) {
		r, err := Project(Source{RestType: "roles", Name: "r", OID: "22222222-2222-4222-8222-222222222222", Body: map[string]any{
			"name": "r", "oid": "22222222-2222-4222-8222-222222222222",
			"documentation": "see wiki",
			"activation":    map[string]any{"administrativeStatus": "enabled", "validTo": "2030-01-01T00:00:00Z"},
			"description":   map[string]any{"odd": "not a string"},
			"assignment": []any{
				map[string]any{"@id": 1.0, "targetRef": map[string]any{"oid": "33333333-3333-4333-8333-333333333333", "type": "RoleType", "relation": "org:default"}, "order": 1.5},
				map[string]any{"@id": 2.0, "targetRef": map[string]any{"oid": "44444444-4444-4444-8444-444444444444", "type": "RoleType", "relation": "org:manager"}, "order": 2.0,
					"condition": map[string]any{"expression": map[string]any{"value": []any{"true"}}}},
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Extra["documentation"] != "see wiki" || r.Extra["activation"] == nil || r.Extra["description"] == nil {
			t.Errorf("extra = %v", r.Extra)
		}
		if len(blocksOf(r, "activation")) != 0 {
			t.Error("activation with validity became a block")
		}
		// The non-integer order cannot be a block: the value is kept whole
		// in extra, without its @id.
		list, _ := r.Extra["assignment"].([]any)
		if len(list) != 1 {
			t.Fatalf("extra assignment = %v", r.Extra["assignment"])
		}
		if _, has := list[0].(map[string]any)["@id"]; has {
			t.Error("@id kept")
		}
		as := blocksOf(r, "assignment")
		if len(as) != 1 {
			t.Fatalf("assignments = %v", r.Blocks)
		}
		expectAttrs(t, as[0].Blocks[0].Attrs, map[string]any{"oid": "44444444-4444-4444-8444-444444444444", "type": "RoleType", "relation": "org:manager"})
		expectAttrs(t, as[0].Attrs, map[string]any{"order": 2, "condition": map[string]any{"expression": map[string]any{"value": []any{"true"}}}})
		if _, has := r.Extra["oid"]; has {
			t.Error("extra holds oid")
		}
		found := false
		for _, n := range r.Notes {
			if n == "extra: activation, assignment, description, documentation" {
				found = true
			}
		}
		if !found {
			t.Errorf("notes = %q", r.Notes)
		}
	})

	t.Run("activation and construction", func(t *testing.T) {
		r, _ := Project(Source{RestType: "services", Body: map[string]any{
			"name":       "s",
			"activation": map[string]any{"administrativeStatus": "disabled"},
			"assignment": []any{map[string]any{"construction": map[string]any{
				"resourceRef": map[string]any{"oid": "55555555-5555-4555-8555-555555555555", "type": "ResourceType", "relation": "org:default"},
				"kind":        "account", "intent": "default",
				"attribute": []any{map[string]any{"ref": "ri:x"}},
			}}},
		}})
		act := blocksOf(r, "activation")
		if len(act) != 1 || act[0].Attrs["administrative_status"] != "disabled" {
			t.Fatalf("activation = %v", r.Blocks)
		}
		as := blocksOf(r, "assignment")
		if len(as) != 1 || len(as[0].Blocks) != 1 || as[0].Blocks[0].Type != "construction" {
			t.Fatalf("assignments = %v", r.Blocks)
		}
		expectAttrs(t, as[0].Blocks[0].Attrs, map[string]any{
			"resource": "55555555-5555-4555-8555-555555555555", "kind": "account", "intent": "default",
			"body": map[string]any{"attribute": []any{map[string]any{"ref": "ri:x"}}},
		})
	})

	t.Run("archetype assignment with more stays a block", func(t *testing.T) {
		r, _ := Project(Source{RestType: "roles", Body: map[string]any{
			"name": "r",
			"assignment": []any{map[string]any{
				"targetRef": map[string]any{"oid": "5b0d4e7a-2c19-4f83-b6a0-71e3c8d94f02", "type": "ArchetypeType", "relation": "org:default"},
				"order":     1.0,
			}},
		}})
		if _, has := r.Attrs["archetype"]; has || len(blocksOf(r, "assignment")) != 1 {
			t.Errorf("attrs/blocks = %v/%v", r.Attrs, r.Blocks)
		}
	})

	t.Run("no body is an error", func(t *testing.T) {
		if _, err := Project(Source{RestType: "roles"}); err == nil {
			t.Error("want error")
		}
	})
}

func TestFilterText(t *testing.T) {
	eq := func(path string, v any) map[string]any {
		return map[string]any{"path": path, "value": []any{v}}
	}
	tests := []struct {
		name   string
		filter any
		want   string
		ok     bool
	}{
		{"text", map[string]any{"text": `name = "x"`}, `name = "x"`, true},
		{"equal", map[string]any{"equal": eq("name", "a \"b\"")}, `name = "a \"b\""`, true},
		{"and", map[string]any{"and": map[string]any{"equal": []any{eq("name", "a"), eq("order", 2.0)}}}, `name = "a" and order = 2`, true},
		{"or in and", map[string]any{"and": map[string]any{"equal": eq("a", "1"), "or": map[string]any{"equal": []any{eq("b", "2"), eq("c", true)}}}},
			`a = "1" and (b = "2" or c = true)`, true},
		{"substring", map[string]any{"substring": eq("name", "a")}, "", false},
		{"matching", map[string]any{"equal": map[string]any{"path": "name", "value": []any{"a"}, "matching": "polyStringNorm"}}, "", false},
		{"two values", map[string]any{"equal": map[string]any{"path": "name", "value": []any{"a", "b"}}}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := filterText(tt.filter)
			if ok != tt.ok || got != tt.want {
				t.Errorf("filterText = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestLabel(t *testing.T) {
	tests := []struct{ name, in, oid, want string }{
		{"plain", "Vault", "", "vault"},
		{"slash and spaces", "vault / operator", "", "vault_operator"},
		{"repeats collapse", "A -- B   C", "", "a_b_c"},
		{"trim", "  --Hello World!-- ", "", "hello_world"},
		{"leading digit", "2fa Policy", "", "_2fa_policy"},
		{"no name", "", "3f6a1c52-8d0e-4b7a-9a41-5c2e7d9b0a11", "_3f6a1c52_8d0e_4b7a_9a41_5c2e7d9b0a11"},
		{"no name letter oid", "", "ab6a1c52-8d0e-4b7a-9a41-5c2e7d9b0a11", "ab6a1c52_8d0e_4b7a_9a41_5c2e7d9b0a11"},
		{"name without letters", "!!!", "ab6a1c52-8d0e", "ab6a1c52_8d0e"},
		{"non ascii", "Zürich Büro", "", "z_rich_b_ro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Label(tt.in, tt.oid); got != tt.want {
				t.Errorf("Label(%q, %q) = %q, want %q", tt.in, tt.oid, got, tt.want)
			}
		})
	}
}

func TestDedupeLabels(t *testing.T) {
	rs := []Resource{{Label: "a"}, {Label: "a"}, {Label: "b"}, {Label: "a"}, {Label: "a_2"}}
	DedupeLabels(rs)
	var got []string
	for _, r := range rs {
		got = append(got, r.Label)
	}
	want := []string{"a", "a_2", "b", "a_3", "a_2_2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
}
