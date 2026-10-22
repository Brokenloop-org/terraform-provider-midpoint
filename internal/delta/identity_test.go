package delta

import (
	"encoding/json"
	"testing"
)

func TestKey(t *testing.T) {
	ref := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	filter := func(text string) map[string]any {
		return map[string]any{"text": text}
	}

	tests := []struct {
		name  string
		item  string
		a, b  any
		equal bool
	}{
		{
			name:  "targetRef_oid",
			item:  "targetRef",
			a:     ref("oid", "r1", "type", "RoleType", "relation", "org:default"),
			b:     ref("oid", "r1", "type", "RoleType", "relation", "org:default", "filter", map[string]any{"filter": filter(`name = "x"`)}),
			equal: true,
		},
		{
			name: "targetRef_oid_other_oid",
			item: "targetRef",
			a:    ref("oid", "r1", "type", "RoleType", "relation", "org:default"),
			b:    ref("oid", "r2", "type", "RoleType", "relation", "org:default"),
		},
		{
			name: "targetRef_oid_other_relation",
			item: "targetRef",
			a:    ref("oid", "r1", "type", "RoleType", "relation", "org:default"),
			b:    ref("oid", "r1", "type", "RoleType", "relation", "org:manager"),
		},
		{
			name:  "targetRef_missing_relation_is_default",
			item:  "targetRef",
			a:     ref("oid", "r1", "type", "RoleType"),
			b:     ref("oid", "r1", "type", "RoleType", "relation", "org:default"),
			equal: true,
		},
		{
			name:  "targetRef_filter",
			item:  "targetRef",
			a:     ref("filter", map[string]any{"filter": filter(`name = "x"`)}, "type", "RoleType", "relation", "org:default"),
			b:     ref("filter", map[string]any{"filter": filter(`name = "x"`)}, "type", "RoleType", "relation", "org:default"),
			equal: true,
		},
		{
			name: "targetRef_filter_other_text",
			item: "targetRef",
			a:    ref("filter", map[string]any{"filter": filter(`name = "x"`)}, "type", "RoleType", "relation", "org:default"),
			b:    ref("filter", map[string]any{"filter": filter(`name = "y"`)}, "type", "RoleType", "relation", "org:default"),
		},
		{
			name: "targetRef_filter_vs_oid",
			item: "targetRef",
			a:    ref("filter", map[string]any{"filter": filter(`name = "x"`)}, "type", "RoleType", "relation", "org:default"),
			b:    ref("oid", "r1", "type", "RoleType", "relation", "org:default"),
		},
		{
			name: "assignment_by_targetRef",
			item: "assignment",
			a: map[string]any{"@id": json.Number("3"), "description": "old",
				"targetRef": ref("oid", "r1", "type", "RoleType", "relation", "org:default")},
			b: map[string]any{"description": "new",
				"targetRef": ref("oid", "r1", "type", "RoleType", "relation", "org:default")},
			equal: true,
		},
		{
			name: "assignment_order_is_identity",
			item: "inducement",
			a: map[string]any{"order": json.Number("2"),
				"targetRef": ref("oid", "r1", "type", "RoleType", "relation", "org:default")},
			b: map[string]any{"order": json.Number("1"),
				"targetRef": ref("oid", "r1", "type", "RoleType", "relation", "org:default")},
		},
		{
			name: "assignment_order_numeric",
			item: "inducement",
			a: map[string]any{"order": json.Number("2"),
				"targetRef": ref("oid", "r1", "type", "RoleType", "relation", "org:default")},
			b: map[string]any{"order": 2.0,
				"targetRef": ref("oid", "r1", "type", "RoleType", "relation", "org:default")},
			equal: true,
		},
		{
			name: "construction",
			item: "inducement",
			a: map[string]any{"@id": json.Number("5"), "construction": map[string]any{
				"resourceRef": ref("oid", "res1", "type", "ResourceType", "relation", "org:default"),
				"kind":        "account", "intent": "default",
				"attribute": []any{map[string]any{"ref": "ri:cn"}},
			}},
			b: map[string]any{"construction": map[string]any{
				"resourceRef": ref("oid", "res1", "relation", "org:default"),
				"kind":        "account", "intent": "default",
			}},
			equal: true,
		},
		{
			name: "construction_other_intent",
			item: "inducement",
			a: map[string]any{"construction": map[string]any{
				"resourceRef": ref("oid", "res1"), "kind": "account", "intent": "default"}},
			b: map[string]any{"construction": map[string]any{
				"resourceRef": ref("oid", "res1"), "kind": "account", "intent": "admin"}},
		},
		{
			name: "construction_resource_filter",
			item: "inducement",
			a: map[string]any{"construction": map[string]any{
				"resourceRef": ref("filter", map[string]any{"filter": filter(`name = "ldap"`)}), "kind": "account"}},
			b: map[string]any{"construction": map[string]any{
				"resourceRef": ref("filter", map[string]any{"filter": filter(`name = "ldap"`)}), "kind": "account", "strength": "strong"}},
			equal: true,
		},
		{
			name:  "objectType",
			item:  "objectType",
			a:     map[string]any{"@id": json.Number("2"), "kind": "account", "intent": "default", "attribute": []any{}},
			b:     map[string]any{"kind": "account", "intent": "default", "displayName": "Accounts"},
			equal: true,
		},
		{
			name: "objectType_other_kind",
			item: "objectType",
			a:    map[string]any{"kind": "account", "intent": "default"},
			b:    map[string]any{"kind": "entitlement", "intent": "default"},
		},
		{
			name:  "attribute",
			item:  "attribute",
			a:     map[string]any{"@id": json.Number("4"), "ref": "ri:cn", "outbound": []any{map[string]any{"strength": "strong"}}},
			b:     map[string]any{"ref": "ri:cn"},
			equal: true,
		},
		{
			name: "attribute_other_ref",
			item: "association",
			a:    map[string]any{"ref": "ri:group"},
			b:    map[string]any{"ref": "ri:member"},
		},
		{
			name:  "fallback_json",
			item:  "authorization",
			a:     map[string]any{"@id": json.Number("9"), "action": []any{"read", "add"}, "decision": "allow", "order": json.Number("1")},
			b:     map[string]any{"decision": "allow", "order": 1.0, "action": []any{"read", "add"}},
			equal: true,
		},
		{
			name: "fallback_json_differs",
			item: "authorization",
			a:    map[string]any{"action": []any{"read"}, "decision": "allow"},
			b:    map[string]any{"action": []any{"read"}, "decision": "deny"},
		},
		{
			name: "fallback_json_string_is_not_number",
			item: "authorization",
			a:    map[string]any{"order": "1"},
			b:    map[string]any{"order": json.Number("1")},
		},
		{
			name:  "fallback_json_nested_ids_ignored",
			item:  "mapping",
			a:     map[string]any{"name": "m", "source": []any{map[string]any{"@id": json.Number("1"), "path": "name"}}},
			b:     map[string]any{"name": "m", "source": []any{map[string]any{"path": "name"}}},
			equal: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ka, kb := Key(tt.item, tt.a), Key(tt.item, tt.b)
			if (ka == kb) != tt.equal {
				t.Errorf("Key(%q, a) = %q\nKey(%q, b) = %q\nequal = %v, want %v", tt.item, ka, tt.item, kb, ka == kb, tt.equal)
			}
		})
	}
}

func TestKey_listsAreSets(t *testing.T) {
	a := map[string]any{"action": []any{"read", "add"}}
	b := map[string]any{"action": []any{"add", "read"}}
	if Key("authorization", a) != Key("authorization", b) {
		t.Error("value order inside a multi-valued item changed the key")
	}
}
