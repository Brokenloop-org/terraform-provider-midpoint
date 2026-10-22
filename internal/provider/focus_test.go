package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

const (
	fArchetypeOID = "00000000-0000-0000-0000-000000000329"
	fServiceOID   = "5e000000-0000-4000-8000-000000000001"
	fRoleOID      = "5e000000-0000-4000-8000-000000000002"
	fResourceOID  = "5e000000-0000-4000-8000-000000000003"
)

func str(s string) types.String { return types.StringValue(s) }

// dyn builds a dynamic value from a Go value in the normalize shape.
func dyn(t *testing.T, v any) types.Dynamic {
	t.Helper()
	d, diags := goToDyn(context.Background(), v)
	noErrors(t, diags)
	return d
}

// baseModel is a focus model with only the computed and common attributes
// set, as they are after a create.
func baseModel() focusModel {
	return focusModel{
		ID: str(fRoleOID), OID: str(fRoleOID),
		DisplayName: types.StringNull(), Description: types.StringNull(), Identifier: types.StringNull(),
		LifecycleState: types.StringNull(), RiskLevel: types.StringNull(),
		Requestable: types.BoolNull(), Delegable: types.BoolNull(),
		Archetype:     types.StringNull(),
		Assignments:   []assignmentModel{},
		Inducements:   []assignmentModel{},
		Authorization: types.DynamicNull(), Extension: types.DynamicNull(), Extra: types.DynamicNull(),
		AdoptExisting: types.BoolValue(false), ForceDestroy: types.BoolValue(false),
		Options:         types.ListNull(types.StringType),
		Tenant:          types.BoolNull(),
		HolderType:      types.ListNull(types.StringType),
		ArchetypePolicy: types.DynamicNull(),
	}
}

func target(oid, typ string) *targetRef {
	return &targetRef{OID: str(oid), Filter: types.StringNull(), Type: str(typ), Relation: str(defaultRelation)}
}

func filterTarget(filter, typ string) *targetRef {
	return &targetRef{OID: types.StringNull(), Filter: str(filter), Type: str(typ), Relation: str(defaultRelation)}
}

func block(tg *targetRef) assignmentModel {
	return assignmentModel{
		Target: tg, Order: types.Int64Null(),
		Condition: types.StringNull(), PolicyRule: types.StringNull(), Extra: types.StringNull(),
	}
}

type focusCase struct {
	name string
	kind focusKind
	raw  bool
	m    func(t *testing.T) focusModel
	want map[string]any
}

func focusCases() []focusCase {
	return []focusCase{
		{
			name: "service_minimal", kind: serviceKind, raw: true,
			m: func(*testing.T) focusModel {
				m := baseModel()
				m.Name, m.Archetype = str("svc"), str(fArchetypeOID)
				return m
			},
			want: map[string]any{
				"name": "svc",
				"assignment": []any{map[string]any{"targetRef": map[string]any{
					"oid": fArchetypeOID, "type": "ArchetypeType", "relation": "org:default",
				}}},
				"archetypeRef":      map[string]any{"oid": fArchetypeOID, "type": "ArchetypeType", "relation": "org:default"},
				"roleMembershipRef": map[string]any{"oid": fArchetypeOID, "type": "ArchetypeType", "relation": "org:default"},
			},
		},
		{
			name: "service_minimal_not_raw", kind: serviceKind,
			m: func(*testing.T) focusModel {
				m := baseModel()
				m.Name, m.Archetype = str("svc"), str(fArchetypeOID)
				return m
			},
			want: map[string]any{
				"name": "svc",
				"assignment": []any{map[string]any{"targetRef": map[string]any{
					"oid": fArchetypeOID, "type": "ArchetypeType", "relation": "org:default",
				}}},
			},
		},
		{
			name: "role_with_identifier_and_inducement", kind: roleKind,
			m: func(*testing.T) focusModel {
				m := baseModel()
				m.Name, m.DisplayName, m.Description, m.Identifier = str("svc / admin"), str("Admin"), str("d"), str("admin")
				m.LifecycleState, m.RiskLevel = str("active"), str("high")
				m.Requestable, m.Delegable = types.BoolValue(true), types.BoolValue(false)
				m.Activation = &activationModel{AdministrativeStatus: str("enabled")}
				m.Inducements = []assignmentModel{block(target(fServiceOID, "ServiceType"))}
				return m
			},
			want: map[string]any{
				"name": "svc / admin", "displayName": "Admin", "description": "d", "identifier": "admin",
				"lifecycleState": "active", "riskLevel": "high", "requestable": true, "delegable": false,
				"activation": map[string]any{"administrativeStatus": "enabled"},
				"inducement": []any{map[string]any{"targetRef": map[string]any{"oid": fServiceOID, "type": "ServiceType"}}},
			},
		},
		{
			name: "business_role_filter_inducement", kind: roleKind,
			m: func(*testing.T) focusModel {
				m := baseModel()
				m.Name = str("business")
				ind := block(filterTarget(`name = "vault / operator"`, "RoleType"))
				ind.Order = types.Int64Value(2)
				ind.Condition = str(`{"expression":{"script":[{"code":"true"}]}}`)
				m.Inducements = []assignmentModel{ind, {
					Construction: &constructionModel{
						Resource: str(fResourceOID), Kind: str("account"), Intent: str("default"),
						Body: str(`{"attribute":[{"ref":"ri:cn"}]}`),
					},
					Order: types.Int64Null(), Condition: types.StringNull(), PolicyRule: types.StringNull(),
					Extra: str(`{"description":"ldap account"}`),
				}}
				return m
			},
			want: map[string]any{
				"name": "business",
				"inducement": []any{
					map[string]any{
						"targetRef": map[string]any{"type": "RoleType", "filter": map[string]any{"text": `name = "vault / operator"`}},
						"order":     2,
						"condition": map[string]any{"expression": map[string]any{"script": []any{map[string]any{"code": "true"}}}},
					},
					map[string]any{
						"construction": map[string]any{
							"resourceRef": map[string]any{"oid": fResourceOID, "type": "ResourceType"},
							"kind":        "account", "intent": "default",
							"attribute": []any{map[string]any{"ref": "ri:cn"}},
						},
						"description": "ldap account",
					},
				},
			},
		},
		{
			name: "org_tenant", kind: orgKind,
			m: func(t *testing.T) focusModel {
				m := baseModel()
				m.Name, m.Tenant = str("tenant-a"), types.BoolValue(true)
				m.Authorization = dyn(t, []any{map[string]any{"action": []any{"urn:x#read"}}})
				m.Assignments = []assignmentModel{block(target(fRoleOID, "OrgType"))}
				m.Assignments[0].Target.Relation = str("org:manager")
				return m
			},
			want: map[string]any{
				"name": "tenant-a", "tenant": true,
				"authorization": []any{map[string]any{"action": []any{"urn:x#read"}}},
				"assignment": []any{map[string]any{"targetRef": map[string]any{
					"oid": fRoleOID, "type": "OrgType", "relation": "org:manager",
				}}},
			},
		},
		{
			name: "archetype_holder_type", kind: archetypeKind,
			m: func(t *testing.T) focusModel {
				m := baseModel()
				m.Name = str("Cluster")
				m.HolderType = types.ListValueMust(types.StringType, []attr.Value{str("RoleType")})
				m.ArchetypePolicy = dyn(t, map[string]any{"display": map[string]any{"label": "Cluster"}})
				return m
			},
			want: map[string]any{
				"name": "Cluster",
				"assignment": []any{map[string]any{
					"identifier":         "holderType",
					"assignmentRelation": []any{map[string]any{"holderType": []any{"#RoleType"}}},
				}},
				"archetypePolicy": map[string]any{"display": map[string]any{"label": "Cluster"}},
			},
		},
		{
			name: "extension_and_extra_merged", kind: roleKind,
			m: func(t *testing.T) focusModel {
				m := baseModel()
				m.Name = str("ext")
				m.Extension = dyn(t, map[string]any{"costCenter": "CC-123", "tags": []any{"a", "b"}})
				m.Extra = dyn(t, map[string]any{
					"subtype":    []any{"x"},
					"inducement": []any{map[string]any{"targetRef": map[string]any{"oid": fRoleOID, "type": "RoleType", "description": "kept"}}},
				})
				m.Inducements = []assignmentModel{block(target(fServiceOID, "ServiceType"))}
				return m
			},
			want: map[string]any{
				"name":      "ext",
				"extension": map[string]any{"costCenter": "CC-123", "tags": []any{"a", "b"}},
				"subtype":   []any{"x"},
				"inducement": []any{
					map[string]any{"targetRef": map[string]any{"oid": fServiceOID, "type": "ServiceType"}},
					map[string]any{"targetRef": map[string]any{"oid": fRoleOID, "type": "RoleType", "description": "kept"}},
				},
			},
		},
	}
}

func TestFocusToBody(t *testing.T) {
	ctx := context.Background()
	for _, tc := range focusCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, diags := focusToBody(ctx, tc.m(t), tc.kind, tc.raw)
			noErrors(t, diags)
			if !reflect.DeepEqual(asJSON(t, got), asJSON(t, tc.want)) {
				t.Errorf("body =\n%s\nwant\n%s", mustJSON(t, got), mustJSON(t, tc.want))
			}
		})
	}

	t.Run("extra_key_conflicts_with_typed_attribute", func(t *testing.T) {
		m := baseModel()
		m.Name, m.Description = str("x"), str("typed")
		m.Extra = dyn(t, map[string]any{"description": "extra"})
		_, diags := focusToBody(ctx, m, roleKind, false)
		if !diags.HasError() || !strings.Contains(diagText(diags), "description") {
			t.Fatalf("want an error naming description, got %q", diagText(diags))
		}
	})

	t.Run("relation_default_omitted", func(t *testing.T) {
		m := baseModel()
		m.Name = str("x")
		m.Assignments = []assignmentModel{block(target(fRoleOID, "OrgType"))}
		got, diags := focusToBody(ctx, m, roleKind, false)
		noErrors(t, diags)
		ref := got["assignment"].([]any)[0].(map[string]any)["targetRef"].(map[string]any)
		if _, ok := ref["relation"]; ok {
			t.Errorf("default relation written: %v", ref)
		}
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// serverView turns a desired body into what midPoint returns for it: JSON
// numbers, container ids, c:-prefixed reference types with the default
// relation and a target name, polystring names, metadata and computed
// activation.
func serverView(t *testing.T, restType string, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	id := 0
	var walk func(name string, v any)
	walk = func(name string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if strings.HasSuffix(name, "Ref") {
				if s, ok := x["type"].(string); ok {
					x["type"] = "c:" + s
				}
				if x["relation"] == nil {
					x["relation"] = "org:default"
				}
				x["targetName"] = "resolved"
			}
			if name == "assignment" || name == "inducement" {
				id++
				x["@id"] = json.Number(strconv.Itoa(id))
				x["activation"] = map[string]any{"effectiveStatus": "enabled"}
			}
			for k, e := range x {
				walk(k, e)
			}
		case []any:
			for _, e := range x {
				walk(name, e)
			}
		}
	}
	for k, v := range out {
		walk(k, v)
	}
	if n, ok := out["name"].(string); ok {
		out["name"] = map[string]any{"orig": n, "norm": strings.ToLower(n)}
	}
	out["oid"] = fRoleOID
	out["version"] = "3"
	out["metadata"] = map[string]any{"createTimestamp": "2026-10-22T12:00:00Z"}
	out["activation"] = mergeMaps(out["activation"], map[string]any{"effectiveStatus": "enabled", "enableTimestamp": "2026-10-22T12:00:00Z"})
	return normalize.Canonical(restType, out)
}

func mergeMaps(a any, b map[string]any) map[string]any {
	out := map[string]any{}
	if m, ok := a.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestFocusFromBody(t *testing.T) {
	ctx := context.Background()
	for _, tc := range focusCases() {
		t.Run("round_trip_"+tc.name, func(t *testing.T) {
			m := tc.m(t)
			body, diags := focusToBody(ctx, m, tc.kind, false)
			noErrors(t, diags)
			actual := serverView(t, tc.kind.Spec.RestType, body)
			got, diags := focusFromBody(ctx, actual, m, tc.kind)
			noErrors(t, diags)
			assertModel(t, got, m)
		})
		t.Run("import_"+tc.name, func(t *testing.T) {
			// After an import the prior holds only the oid: everything
			// comes from the server and must produce the same body.
			m := tc.m(t)
			body, diags := focusToBody(ctx, m, tc.kind, false)
			noErrors(t, diags)
			actual := serverView(t, tc.kind.Spec.RestType, body)
			got, diags := focusFromBody(ctx, actual, importStub(), tc.kind)
			noErrors(t, diags)
			again, diags := focusToBody(ctx, got, tc.kind, false)
			noErrors(t, diags)
			if d, a := normalize.Canonical(tc.kind.Spec.RestType, again), normalize.Canonical(tc.kind.Spec.RestType, body); !normalize.Equal(d, a) {
				t.Errorf("imported model body =\n%s\nwant\n%s", mustJSON(t, d), mustJSON(t, a))
			}
		})
	}

	t.Run("archetype_fold_only_when_prior_set", func(t *testing.T) {
		m := baseModel()
		m.Name, m.Archetype = str("svc"), str(fArchetypeOID)
		body, diags := focusToBody(ctx, m, serviceKind, false)
		noErrors(t, diags)
		actual := serverView(t, "services", body)

		got, diags := focusFromBody(ctx, actual, m, serviceKind)
		noErrors(t, diags)
		if got.Archetype.ValueString() != fArchetypeOID || len(got.Assignments) != 0 {
			t.Errorf("prior archetype set: archetype = %v, assignments = %v", got.Archetype, got.Assignments)
		}

		prior := baseModel()
		prior.Name = str("svc")
		got, diags = focusFromBody(ctx, actual, prior, serviceKind)
		noErrors(t, diags)
		if !got.Archetype.IsNull() || len(got.Assignments) != 1 || got.Assignments[0].Target.OID.ValueString() != fArchetypeOID ||
			got.Assignments[0].Target.Type.ValueString() != "ArchetypeType" {
			t.Errorf("prior archetype null: archetype = %v, assignments = %+v", got.Archetype, got.Assignments)
		}

		got, diags = focusFromBody(ctx, actual, importStub(), serviceKind)
		noErrors(t, diags)
		if got.Archetype.ValueString() != fArchetypeOID || len(got.Assignments) != 0 {
			t.Errorf("import: archetype = %v, assignments = %v", got.Archetype, got.Assignments)
		}
	})

	t.Run("unknown_item_goes_to_extra", func(t *testing.T) {
		prior := baseModel()
		prior.Name = str("r")
		actual := normalize.Canonical("roles", map[string]any{
			"oid":     fRoleOID,
			"name":    "r",
			"subtype": "x",
			"tenant":  true, // not an attribute of midpoint_role
			"inducement": []any{
				map[string]any{"@id": json.Number("1"), "targetRef": map[string]any{"oid": fServiceOID, "type": "c:ServiceType"}},
				// A non-text filter cannot be written in a target block.
				map[string]any{"@id": json.Number("2"), "targetRef": map[string]any{
					"type": "c:RoleType", "filter": map[string]any{"equal": map[string]any{"path": "name", "value": "x"}},
				}},
			},
			"activation": map[string]any{"administrativeStatus": "enabled", "lockoutStatus": "normal"},
		})
		got, diags := focusFromBody(ctx, actual, prior, roleKind)
		noErrors(t, diags)
		extra, diags := dynToGo(ctx, got.Extra)
		noErrors(t, diags)
		want := map[string]any{
			"subtype": "x",
			"tenant":  true,
			"inducement": []any{map[string]any{"targetRef": map[string]any{
				"type": "RoleType", "relation": "org:default",
				"filter": map[string]any{"equal": map[string]any{"path": "name", "value": []any{"x"}}},
			}}},
			"activation": map[string]any{"administrativeStatus": "enabled", "lockoutStatus": "normal"},
		}
		if !reflect.DeepEqual(asJSON(t, extra), asJSON(t, want)) {
			t.Errorf("extra =\n%s\nwant\n%s", mustJSON(t, extra), mustJSON(t, want))
		}
		if len(got.Inducements) != 1 || got.Inducements[0].Target.OID.ValueString() != fServiceOID {
			t.Errorf("inducements = %+v", got.Inducements)
		}
		if got.Activation != nil {
			t.Errorf("activation = %+v, want nil (it is in extra)", got.Activation)
		}
		// Nothing is lost: the model writes back the same object.
		back, diags := focusToBody(ctx, got, roleKind, false)
		noErrors(t, diags)
		if d, a := normalize.Canonical("roles", back), stateBody(actual); !normalize.Equal(d, a) {
			t.Errorf("written back =\n%s\nwant\n%s", mustJSON(t, d), mustJSON(t, a))
		}
	})

	t.Run("keeps_configured_json_text_when_equal", func(t *testing.T) {
		m := baseModel()
		m.Name = str("r")
		ind := block(target(fServiceOID, "ServiceType"))
		// Spacing, key order and a single value where midPoint returns a
		// list: canonically equal, so the configured text stays.
		ind.Condition = str(`{ "source": {"path": "name"}, "expression": {"script": [{"code": "true"}]} }`)
		m.Inducements = []assignmentModel{ind}
		body, diags := focusToBody(ctx, m, roleKind, false)
		noErrors(t, diags)
		actual := serverView(t, "roles", body)
		actual["description"] = "changed in the GUI"
		got, diags := focusFromBody(ctx, actual, m, roleKind)
		noErrors(t, diags)
		if got.Inducements[0].Condition != ind.Condition {
			t.Errorf("condition = %s, want the configured text", got.Inducements[0].Condition)
		}
		if got.Description.ValueString() != "changed in the GUI" {
			t.Errorf("description = %s", got.Description)
		}
	})

	t.Run("gui_added_inducement_is_a_block", func(t *testing.T) {
		m := baseModel()
		m.Name = str("r")
		m.Inducements = []assignmentModel{block(target(fServiceOID, "ServiceType"))}
		body, diags := focusToBody(ctx, m, roleKind, false)
		noErrors(t, diags)
		body["inducement"] = append(body["inducement"].([]any), map[string]any{"targetRef": map[string]any{"oid": fRoleOID, "type": "RoleType"}})
		actual := serverView(t, "roles", body)
		got, diags := focusFromBody(ctx, actual, m, roleKind)
		noErrors(t, diags)
		if len(got.Inducements) != 2 || got.Inducements[0].Target.OID.ValueString() != fServiceOID ||
			got.Inducements[1].Target.OID.ValueString() != fRoleOID || !got.Extra.IsNull() {
			t.Errorf("inducements = %+v, extra = %v", got.Inducements, got.Extra)
		}
	})
}

// importStub is the state ImportState leaves: the oid and the defaults.
func importStub() focusModel {
	m := baseModel()
	m.Name = types.StringNull()
	return m
}

// assertModel compares two focus models through their attribute values.
func assertModel(t *testing.T, got, want focusModel) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("model =\n%+v\nwant\n%+v", got, want)
	}
}

func TestFocusValidate_archetypeConflict(t *testing.T) {
	ctx := context.Background()
	m := baseModel()
	m.Name, m.Archetype = str("svc"), str(fArchetypeOID)
	m.Assignments = []assignmentModel{block(target(fArchetypeOID, "ArchetypeType"))}
	diags := validateFocus(ctx, m, serviceKind)
	if !diags.HasError() || !strings.Contains(diagText(diags), "archetype") {
		t.Fatalf("want an archetype conflict error, got %q", diagText(diags))
	}

	m.Assignments[0].Target.Type = str("c:ArchetypeType")
	if diags := validateFocus(ctx, m, serviceKind); !diags.HasError() {
		t.Fatal("c:ArchetypeType not detected")
	}

	m.Archetype = types.StringNull()
	noErrors(t, validateFocus(ctx, m, serviceKind))

	m.Archetype = types.StringUnknown()
	if diags := validateFocus(ctx, m, serviceKind); !diags.HasError() {
		t.Fatal("unknown archetype with an archetype assignment not detected")
	}
}

func TestFocusValidate_conflicts(t *testing.T) {
	ctx := context.Background()
	for name, mod := range map[string]func(m *focusModel){
		"extra_typed_key": func(m *focusModel) {
			m.Description = str("x")
			m.Extra = dyn(t, map[string]any{"description": "y"})
		},
		"extra_oid": func(m *focusModel) {
			m.Extra = dyn(t, map[string]any{"oid": fRoleOID})
		},
		"construction_body_kind": func(m *focusModel) {
			m.Inducements = []assignmentModel{{
				Construction: &constructionModel{Resource: str(fResourceOID), Kind: str("account"), Intent: types.StringNull(), Body: str(`{"kind":"entitlement"}`)},
				Order:        types.Int64Null(), Condition: types.StringNull(), PolicyRule: types.StringNull(), Extra: types.StringNull(),
			}}
		},
		"block_extra_condition": func(m *focusModel) {
			b := block(target(fServiceOID, "ServiceType"))
			b.Condition = str(`{}`)
			b.Extra = str(`{"condition":{}}`)
			m.Inducements = []assignmentModel{b}
		},
		"empty_assignment": func(m *focusModel) {
			m.Inducements = []assignmentModel{block(nil)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := baseModel()
			m.Name = str("r")
			mod(&m)
			if diags := validateFocus(ctx, m, roleKind); !diags.HasError() {
				t.Fatal("want a validation error")
			}
		})
	}
}

func TestTargetValidator(t *testing.T) {
	ctx := context.Background()
	typ := map[string]attr.Type{"oid": types.StringType, "filter": types.StringType, "type": types.StringType, "relation": types.StringType}
	obj := func(oid, filter types.String) types.Object {
		return types.ObjectValueMust(typ, map[string]attr.Value{"oid": oid, "filter": filter, "type": str("RoleType"), "relation": str(defaultRelation)})
	}
	for name, tc := range map[string]struct {
		v       types.Object
		wantErr bool
	}{
		"neither":      {obj(types.StringNull(), types.StringNull()), true},
		"oid":          {obj(str("x"), types.StringNull()), false},
		"filter":       {obj(types.StringNull(), str("name = \"x\"")), false},
		"unknown_oid":  {obj(types.StringUnknown(), types.StringNull()), false},
		"absent_block": {types.ObjectNull(typ), false},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &validator.ObjectResponse{}
			targetValidator{}.ValidateObject(ctx, validator.ObjectRequest{ConfigValue: tc.v}, resp)
			if resp.Diagnostics.HasError() != tc.wantErr {
				t.Errorf("error = %v, want %v: %s", resp.Diagnostics.HasError(), tc.wantErr, diagText(resp.Diagnostics))
			}
		})
	}
}

func TestJSONTextValidator(t *testing.T) {
	ctx := context.Background()
	for text, wantErr := range map[string]bool{
		`{"a":1}`: false, `{}`: false, `{not json`: true, `[1]`: true, `{"a":1} {}`: true,
	} {
		resp := &validator.StringResponse{}
		jsonTextValidator{}.ValidateString(ctx, validator.StringRequest{ConfigValue: str(text)}, resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%s: error = %v, want %v", text, resp.Diagnostics.HasError(), wantErr)
		}
	}
}
