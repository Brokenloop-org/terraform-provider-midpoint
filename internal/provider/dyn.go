package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// numberPrecision matches the precision Terraform uses for numbers (512 bits).
const numberPrecision = 512

// dynToGo converts a Terraform dynamic value to the Go shape used by the
// normalize and delta packages: map[string]any, []any, json.Number, string,
// bool and nil.
//
// A null value (at any depth) becomes nil; nested null object attributes keep
// their key with a nil value. An unknown value (at any depth) yields nil and
// an error diagnostic. Objects and maps become map[string]any; tuples, lists
// and sets become []any (sets in the order the framework returns them).
// Numbers become json.Number holding the shortest exact decimal text, so
// integers render as "2" rather than "2.0".
func dynToGo(ctx context.Context, v types.Dynamic) (any, diag.Diagnostics) {
	var diags diag.Diagnostics
	out, err := attrToGo(ctx, v)
	if err != nil {
		diags.AddError("Cannot convert dynamic value", err.Error())
		return nil, diags
	}
	return out, diags
}

func attrToGo(ctx context.Context, v attr.Value) (any, error) {
	if v == nil || v.IsNull() {
		return nil, nil
	}
	if v.IsUnknown() {
		return nil, fmt.Errorf("value is unknown")
	}
	switch tv := v.(type) {
	case basetypes.DynamicValue:
		if tv.IsUnderlyingValueNull() {
			return nil, nil
		}
		if tv.IsUnderlyingValueUnknown() {
			return nil, fmt.Errorf("value is unknown")
		}
		return attrToGo(ctx, tv.UnderlyingValue())
	case basetypes.StringValue:
		return tv.ValueString(), nil
	case basetypes.BoolValue:
		return tv.ValueBool(), nil
	case basetypes.NumberValue:
		return json.Number(tv.ValueBigFloat().Text('f', -1)), nil
	case basetypes.ObjectValue:
		return attrMapToGo(ctx, tv.Attributes())
	case basetypes.MapValue:
		return attrMapToGo(ctx, tv.Elements())
	case basetypes.TupleValue:
		return attrSliceToGo(ctx, tv.Elements())
	case basetypes.ListValue:
		return attrSliceToGo(ctx, tv.Elements())
	case basetypes.SetValue:
		return attrSliceToGo(ctx, tv.Elements())
	default:
		return nil, fmt.Errorf("unsupported value type %s", v.Type(ctx))
	}
}

func attrMapToGo(ctx context.Context, in map[string]attr.Value) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for k, e := range in {
		g, err := attrToGo(ctx, e)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = g
	}
	return out, nil
}

func attrSliceToGo(ctx context.Context, in []attr.Value) ([]any, error) {
	out := make([]any, len(in))
	for i, e := range in {
		g, err := attrToGo(ctx, e)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = g
	}
	return out, nil
}

// goToDyn converts a Go value in the normalize/delta shape back to a
// Terraform dynamic value, reproducing the types Terraform infers for HCL
// literals and yamldecode output: map[string]any becomes an Object with
// per-key attribute types, []any becomes a Tuple with per-element types,
// json.Number/int/int64/float64 become Number, nil becomes types.DynamicNull().
//
// A nil nested inside an object or tuple becomes types.DynamicNull() with
// attribute/element type types.DynamicType. That keeps the enclosing value
// constructible (a null has no concrete type) and matches what yamldecode
// produces for a YAML null, whose type is DynamicPseudoType.
func goToDyn(ctx context.Context, v any) (types.Dynamic, diag.Diagnostics) {
	var diags diag.Diagnostics
	if v == nil {
		return types.DynamicNull(), diags
	}
	out, err := goToAttr(ctx, v)
	if err != nil {
		diags.AddError("Cannot convert value to dynamic", err.Error())
		return types.DynamicNull(), diags
	}
	return types.DynamicValue(out), diags
}

func goToAttr(ctx context.Context, v any) (attr.Value, error) {
	switch tv := v.(type) {
	case nil:
		return types.DynamicNull(), nil
	case string:
		return types.StringValue(tv), nil
	case bool:
		return types.BoolValue(tv), nil
	case json.Number:
		f, _, err := big.ParseFloat(string(tv), 10, numberPrecision, big.ToNearestEven)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", string(tv), err)
		}
		return types.NumberValue(f), nil
	case int:
		return types.NumberValue(new(big.Float).SetPrec(numberPrecision).SetInt64(int64(tv))), nil
	case int64:
		return types.NumberValue(new(big.Float).SetPrec(numberPrecision).SetInt64(tv)), nil
	case float64:
		if math.IsNaN(tv) || math.IsInf(tv, 0) {
			return nil, fmt.Errorf("invalid number %v", tv)
		}
		return types.NumberValue(new(big.Float).SetPrec(numberPrecision).SetFloat64(tv)), nil
	case map[string]any:
		attrTypes := make(map[string]attr.Type, len(tv))
		attrs := make(map[string]attr.Value, len(tv))
		for k, e := range tv {
			a, err := goToAttr(ctx, e)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			attrTypes[k] = a.Type(ctx)
			attrs[k] = a
		}
		obj, d := types.ObjectValue(attrTypes, attrs)
		if d.HasError() {
			return nil, fmt.Errorf("building object: %v", d)
		}
		return obj, nil
	case []any:
		elemTypes := make([]attr.Type, len(tv))
		elems := make([]attr.Value, len(tv))
		for i, e := range tv {
			a, err := goToAttr(ctx, e)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			elemTypes[i] = a.Type(ctx)
			elems[i] = a
		}
		tup, d := types.TupleValue(elemTypes, elems)
		if d.HasError() {
			return nil, fmt.Errorf("building tuple: %v", d)
		}
		return tup, nil
	default:
		return nil, fmt.Errorf("unsupported Go type %T", v)
	}
}
