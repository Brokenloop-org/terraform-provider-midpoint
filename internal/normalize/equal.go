package normalize

import (
	"encoding/json"
	"math/big"
	"reflect"
)

// Equal reports whether two canonical values are equal. Maps are compared
// key by key ignoring "@id" (a missing key equals a nil value), lists
// positionally (set semantics live in package delta), and numbers by value
// across json.Number, floats and integers. A string never equals a number.
func Equal(a, b any) bool {
	if isNumber(a) || isNumber(b) {
		return NumberEqual(a, b)
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range x {
			if k != "@id" && !Equal(v, y[k]) {
				return false
			}
		}
		for k, v := range y {
			if _, seen := x[k]; !seen && k != "@id" && !Equal(nil, v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

// NumberEqual reports whether a and b are both numbers (json.Number, a float
// or an integer type) with the same value. Comparison is exact, so large
// json.Number values do not lose precision.
func NumberEqual(a, b any) bool {
	x, ok := toRat(a)
	if !ok {
		return false
	}
	y, ok := toRat(b)
	if !ok {
		return false
	}
	return x.Cmp(y) == 0
}

func isNumber(v any) bool {
	switch v.(type) {
	case json.Number, float32, float64,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

func toRat(v any) (*big.Rat, bool) {
	r := new(big.Rat)
	switch n := v.(type) {
	case json.Number:
		return r.SetString(n.String())
	case float64:
		return ratFromFloat(n)
	case float32:
		return ratFromFloat(float64(n))
	case int:
		return r.SetInt64(int64(n)), true
	case int8:
		return r.SetInt64(int64(n)), true
	case int16:
		return r.SetInt64(int64(n)), true
	case int32:
		return r.SetInt64(int64(n)), true
	case int64:
		return r.SetInt64(n), true
	case uint:
		return r.SetUint64(uint64(n)), true
	case uint8:
		return r.SetUint64(uint64(n)), true
	case uint16:
		return r.SetUint64(uint64(n)), true
	case uint32:
		return r.SetUint64(uint64(n)), true
	case uint64:
		return r.SetUint64(n), true
	}
	return nil, false
}

// ratFromFloat converts a finite float; NaN and infinities are not numbers
// that compare equal to anything.
func ratFromFloat(f float64) (*big.Rat, bool) {
	r := new(big.Rat).SetFloat64(f)
	return r, r != nil
}
