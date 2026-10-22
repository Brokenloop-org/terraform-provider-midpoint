package provider

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// num builds a types.Number from decimal text at Terraform's 512-bit precision.
func num(t *testing.T, s string) types.Number {
	t.Helper()
	f, _, err := big.ParseFloat(s, 10, 512, big.ToNearestEven)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return types.NumberValue(f)
}

func TestDyn_roundtrip(t *testing.T) {
	ctx := context.Background()

	innerType := map[string]attr.Type{"x": types.NumberType, "y": types.BoolType}
	inner := types.ObjectValueMust(innerType, map[string]attr.Value{
		"x": num(t, "1"),
		"y": types.BoolValue(true),
	})
	nestedObject := types.ObjectValueMust(
		map[string]attr.Type{"name": types.StringType, "inner": types.ObjectType{AttrTypes: innerType}},
		map[string]attr.Value{"name": types.StringValue("n"), "inner": inner},
	)

	objA := types.ObjectValueMust(map[string]attr.Type{"k": types.StringType},
		map[string]attr.Value{"k": types.StringValue("a")})
	objB := types.ObjectValueMust(map[string]attr.Type{"k": types.NumberType, "z": types.BoolType},
		map[string]attr.Value{"k": num(t, "3"), "z": types.BoolValue(false)})
	tupleOfObjects := types.TupleValueMust(
		[]attr.Type{objA.Type(ctx), objB.Type(ctx)},
		[]attr.Value{objA, objB},
	)

	stringList := types.TupleValueMust(
		[]attr.Type{types.StringType, types.StringType},
		[]attr.Value{types.StringValue("a"), types.StringValue("b")},
	)

	// yamldecode of `{a: null, b: [null]}` types the nulls as DynamicPseudoType.
	withNulls := types.ObjectValueMust(
		map[string]attr.Type{
			"a": types.DynamicType,
			"b": types.TupleType{ElemTypes: []attr.Type{types.DynamicType}},
		},
		map[string]attr.Value{
			"a": types.DynamicNull(),
			"b": types.TupleValueMust([]attr.Type{types.DynamicType}, []attr.Value{types.DynamicNull()}),
		},
	)

	cases := []struct {
		name string
		in   types.Dynamic
		want any
	}{
		{
			name: "nested_object",
			in:   types.DynamicValue(nestedObject),
			want: map[string]any{"name": "n", "inner": map[string]any{"x": json.Number("1"), "y": true}},
		},
		{
			name: "tuple_of_objects",
			in:   types.DynamicValue(tupleOfObjects),
			want: []any{
				map[string]any{"k": "a"},
				map[string]any{"k": json.Number("3"), "z": false},
			},
		},
		{name: "number_int", in: types.DynamicValue(num(t, "2")), want: json.Number("2")},
		{name: "number_decimal", in: types.DynamicValue(num(t, "2.5")), want: json.Number("2.5")},
		{name: "number_negative", in: types.DynamicValue(num(t, "-7")), want: json.Number("-7")},
		{name: "number_19_digits", in: types.DynamicValue(num(t, "1234567890123456789")), want: json.Number("1234567890123456789")},
		{name: "number_long_decimal", in: types.DynamicValue(num(t, "123456789.123456789")), want: json.Number("123456789.123456789")},
		{name: "number_point_one", in: types.DynamicValue(num(t, "0.1")), want: json.Number("0.1")},
		{
			name: "empty_object",
			in:   types.DynamicValue(types.ObjectValueMust(map[string]attr.Type{}, map[string]attr.Value{})),
			want: map[string]any{},
		},
		{
			name: "empty_tuple",
			in:   types.DynamicValue(types.TupleValueMust([]attr.Type{}, []attr.Value{})),
			want: []any{},
		},
		{name: "string_list", in: types.DynamicValue(stringList), want: []any{"a", "b"}},
		{name: "string", in: types.DynamicValue(types.StringValue("s")), want: "s"},
		{name: "bool", in: types.DynamicValue(types.BoolValue(false)), want: false},
		{
			name: "nested_nulls",
			in:   types.DynamicValue(withNulls),
			want: map[string]any{"a": nil, "b": []any{nil}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, diags := dynToGo(ctx, tc.in)
			if diags.HasError() {
				t.Fatalf("dynToGo diags: %v", diags)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("dynToGo = %#v, want %#v", got, tc.want)
			}
			back, diags := goToDyn(ctx, got)
			if diags.HasError() {
				t.Fatalf("goToDyn diags: %v", diags)
			}
			if !back.Equal(tc.in) {
				t.Fatalf("round trip mismatch:\n got  %s\n want %s", back, tc.in)
			}
			if !back.UnderlyingValue().Type(ctx).Equal(tc.in.UnderlyingValue().Type(ctx)) {
				t.Fatalf("round trip type mismatch: got %s want %s",
					back.UnderlyingValue().Type(ctx), tc.in.UnderlyingValue().Type(ctx))
			}
		})
	}
}

func TestDyn_nullAndUnknown(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		in      types.Dynamic
		wantErr string
	}{
		{name: "null_dynamic", in: types.DynamicNull()},
		{name: "null_underlying", in: types.DynamicValue(types.StringNull())},
		{name: "unknown_dynamic", in: types.DynamicUnknown(), wantErr: "value is unknown"},
		{name: "unknown_underlying", in: types.DynamicValue(types.StringUnknown()), wantErr: "value is unknown"},
		{
			name: "unknown_nested",
			in: types.DynamicValue(types.ObjectValueMust(
				map[string]attr.Type{"a": types.StringType},
				map[string]attr.Value{"a": types.StringUnknown()},
			)),
			wantErr: "value is unknown",
		},
		{
			name: "null_nested_attribute_keeps_key",
			in: types.DynamicValue(types.ObjectValueMust(
				map[string]attr.Type{"a": types.StringType},
				map[string]attr.Value{"a": types.StringNull()},
			)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, diags := dynToGo(ctx, tc.in)
			if tc.wantErr != "" {
				if !diags.HasError() {
					t.Fatalf("expected error diagnostic, got none (value %#v)", got)
				}
				if !strings.Contains(diags.Errors()[0].Summary()+diags.Errors()[0].Detail(), tc.wantErr) {
					t.Fatalf("diag = %v, want containing %q", diags, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("got %#v, want nil", got)
				}
				return
			}
			if diags.HasError() {
				t.Fatalf("unexpected diags: %v", diags)
			}
			if tc.name == "null_nested_attribute_keeps_key" {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("got %#v, want map", got)
				}
				if v, present := m["a"]; !present || v != nil {
					t.Fatalf("got %#v, want key a with nil", m)
				}
				return
			}
			if got != nil {
				t.Fatalf("got %#v, want nil", got)
			}
		})
	}
}

func TestDyn_dynToGoCollections(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		in   types.Dynamic
		want any
	}{
		{
			name: "list",
			in: types.DynamicValue(types.ListValueMust(types.StringType,
				[]attr.Value{types.StringValue("a"), types.StringValue("b")})),
			want: []any{"a", "b"},
		},
		{
			name: "set",
			in: types.DynamicValue(types.SetValueMust(types.NumberType,
				[]attr.Value{num(t, "1")})),
			want: []any{json.Number("1")},
		},
		{
			name: "map",
			in: types.DynamicValue(types.MapValueMust(types.BoolType,
				map[string]attr.Value{"a": types.BoolValue(true), "b": types.BoolValue(false)})),
			want: map[string]any{"a": true, "b": false},
		},
		{
			name: "empty_list",
			in:   types.DynamicValue(types.ListValueMust(types.StringType, []attr.Value{})),
			want: []any{},
		},
		{
			name: "nested_dynamic_in_object",
			in: types.DynamicValue(types.ObjectValueMust(
				map[string]attr.Type{"d": types.DynamicType},
				map[string]attr.Value{"d": types.DynamicValue(types.StringValue("v"))},
			)),
			want: map[string]any{"d": "v"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, diags := dynToGo(ctx, tc.in)
			if diags.HasError() {
				t.Fatalf("diags: %v", diags)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDyn_goToDyn(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		in      any
		want    types.Dynamic
		wantErr string
	}{
		{name: "nil", in: nil, want: types.DynamicNull()},
		{name: "int", in: 5, want: types.DynamicValue(num(t, "5"))},
		{name: "int64", in: int64(-9223372036854775808), want: types.DynamicValue(num(t, "-9223372036854775808"))},
		{name: "float64", in: 2.5, want: types.DynamicValue(num(t, "2.5"))},
		{name: "json_number_19_digits", in: json.Number("9999999999999999999"), want: types.DynamicValue(num(t, "9999999999999999999"))},
		{name: "json_number_exponent", in: json.Number("1e3"), want: types.DynamicValue(num(t, "1000"))},
		{
			name: "list_to_tuple",
			in:   []any{"a", json.Number("1"), true, nil},
			want: types.DynamicValue(types.TupleValueMust(
				[]attr.Type{types.StringType, types.NumberType, types.BoolType, types.DynamicType},
				[]attr.Value{types.StringValue("a"), num(t, "1"), types.BoolValue(true), types.DynamicNull()},
			)),
		},
		{name: "bad_json_number", in: json.Number("abc"), wantErr: "abc"},
		{name: "nan", in: math.NaN(), wantErr: "NaN"},
		{name: "inf", in: math.Inf(1), wantErr: "Inf"},
		{name: "unsupported_type", in: struct{}{}, wantErr: "unsupported"},
		{name: "nested_error", in: map[string]any{"a": []any{json.Number("x")}}, wantErr: "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, diags := goToDyn(ctx, tc.in)
			if tc.wantErr != "" {
				if !diags.HasError() {
					t.Fatalf("expected error, got %s", got)
				}
				e := diags.Errors()[0]
				if !strings.Contains(e.Summary()+" "+e.Detail(), tc.wantErr) {
					t.Fatalf("diag = %v, want containing %q", diags, tc.wantErr)
				}
				return
			}
			if diags.HasError() {
				t.Fatalf("diags: %v", diags)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
