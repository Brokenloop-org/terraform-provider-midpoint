package mpschema

import (
	"encoding/json"
	"testing"
)

func testTable() *Table {
	return &Table{
		Line: "4.9",
		Types: map[string]*Type{
			"Root": {Items: map[string]Item{
				"description": {Kind: KindSimple},
				"objectType":  {Type: "ObjType", Kind: KindComplex, Multi: true},
				"one":         {Type: "ObjType", Kind: KindComplex},
				"strength":    {Type: "Strength", Kind: KindEnum},
				"flag":        {Kind: KindBoolean},
				"count":       {Kind: KindInteger},
				"text":        {Kind: KindSimple},
				"tags":        {Kind: KindSimple, Multi: true},
				"opened":      {Type: "OpenT", Kind: KindComplex},
				"mystery":     {Type: "NotInTable", Kind: KindComplex},
				"anything":    {Kind: KindOpen},
				"appender":    {Type: "AppenderT", Kind: KindComplex},
			}},
			"ObjType":      {Items: map[string]Item{"attribute": {Kind: KindSimple}, "kind": {Kind: KindSimple}}},
			"OpenT":        {Open: true, Items: map[string]Item{"a": {Kind: KindSimple}}},
			"AppenderT":    {Items: map[string]Item{"name": {Kind: KindSimple}}},
			"FileAppender": {Items: map[string]Item{"name": {Kind: KindSimple}, "fileName": {Kind: KindSimple}}},
		},
		Enums: map[string][]string{"Strength": {"strong", "weak", "normal"}},
	}
}

func find(t *testing.T, v any) []Finding {
	t.Helper()
	return testTable().Check("Root", v)
}

func wantKeys(t *testing.T, got []Finding, keys ...string) {
	t.Helper()
	if len(got) != len(keys) {
		t.Fatalf("got %d findings %+v, want keys %v", len(got), got, keys)
	}
	for i, k := range keys {
		if got[i].Key != k {
			t.Errorf("finding %d key = %q, want %q", i, got[i].Key, k)
		}
	}
}

func TestCheck_unknownKey(t *testing.T) {
	got := find(t, map[string]any{"descripton": "x"})
	wantKeys(t, got, "descripton")
	if got[0].Path != "descripton" || got[0].Type != "Root" || got[0].Problem == "" {
		t.Errorf("unexpected finding %+v", got[0])
	}
}

func TestCheck_nestedPathWithIndex(t *testing.T) {
	got := find(t, map[string]any{"objectType": []any{
		map[string]any{"kind": "a"},
		map[string]any{"attibute": "b"},
	}})
	wantKeys(t, got, "attibute")
	if got[0].Path != "objectType[1].attibute" {
		t.Errorf("path = %q", got[0].Path)
	}
}

func TestCheck_annotationsSkipped(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"@ns": "x", "@id": 1, "@metadata": map[string]any{"z": 1}, "description": "d"}))
}

func TestCheck_hashKeyFlagged(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"#costCenter": "x"}), "#costCenter")
}

func TestCheck_namespacedKey(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"c:description": "x"}))
	wantKeys(t, find(t, map[string]any{"c:descripton": "x"}), "c:descripton")
}

func TestCheck_openTypeStops(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"opened": map[string]any{"whatever": map[string]any{"x": 1}}}))
	wantKeys(t, find(t, map[string]any{"anything": map[string]any{"whatever": 1}}))
}

func TestCheck_unknownTypeStops(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"mystery": map[string]any{"whatever": 1}}))
}

func TestCheck_atTypeSelects(t *testing.T) {
	ok := map[string]any{"appender": map[string]any{"@type": "c:FileAppender", "fileName": "f"}}
	wantKeys(t, find(t, ok))
	bad := map[string]any{"appender": map[string]any{"fileName": "f"}}
	wantKeys(t, find(t, bad), "fileName")
	stop := map[string]any{"appender": map[string]any{"@type": "c:Missing", "fileName": "f"}}
	wantKeys(t, find(t, stop))
}

func TestCheck_atValueUnwraps(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"strength": map[string]any{"@value": "strong"}}))
	wantKeys(t, find(t, map[string]any{"strength": map[string]any{"@value": "strng"}}), "strength")
}

func TestCheck_emptiesNeverFlagged(t *testing.T) {
	for _, key := range []string{"description", "objectType", "one", "strength", "flag", "count", "opened", "mystery", "anything"} {
		for _, v := range []any{nil, map[string]any{}, []any{}} {
			if got := find(t, map[string]any{key: v}); len(got) != 0 {
				t.Errorf("%s=%#v: %+v", key, v, got)
			}
		}
	}
	wantKeys(t, find(t, nil))
	wantKeys(t, find(t, map[string]any{}))
}

func TestCheck_unknownSkipsOnlyItself(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"a": Unknown, "descripton": "x"}), "a", "descripton")
	wantKeys(t, find(t, map[string]any{"strength": Unknown, "descripton": "x"}), "descripton")
	wantKeys(t, find(t, map[string]any{"objectType": []any{Unknown, map[string]any{"bad": 1}}}), "bad")
}

func TestCheck_enum(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"strength": "strng"}), "strength")
	wantKeys(t, find(t, map[string]any{"strength": "strong"}))
}

func TestCheck_boolean(t *testing.T) {
	for _, v := range []any{true, false, "true", "false", "1", "0"} {
		wantKeys(t, find(t, map[string]any{"flag": v}))
	}
	wantKeys(t, find(t, map[string]any{"flag": "yes"}), "flag")
}

func TestCheck_integer(t *testing.T) {
	for _, v := range []any{3, int64(3), 3.0, json.Number("3"), "3", "-3"} {
		wantKeys(t, find(t, map[string]any{"count": v}))
	}
	for _, v := range []any{"first", 3.5, json.Number("3.5"), "3.5"} {
		wantKeys(t, find(t, map[string]any{"count": v}), "count")
	}
}

func TestCheck_objectForSimple(t *testing.T) {
	for _, key := range []string{"text", "flag", "count", "strength"} {
		wantKeys(t, find(t, map[string]any{key: map[string]any{"text": "x"}}), key)
		wantKeys(t, find(t, map[string]any{key: map[string]any{"@type": "t", "text": "x"}}))
	}
}

func TestCheck_severalForSingle(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"text": []any{"a", "b"}}), "text")
	wantKeys(t, find(t, map[string]any{"text": []any{"a"}}))
	wantKeys(t, find(t, map[string]any{"tags": []any{"a", "b"}}))
	wantKeys(t, find(t, map[string]any{"one": []any{map[string]any{}, map[string]any{}}}), "one")
}

func TestCheck_scalarForComplex(t *testing.T) {
	wantKeys(t, find(t, map[string]any{"one": "x"}))
}

func TestCheck_findingsSortedByPath(t *testing.T) {
	got := find(t, map[string]any{"zzz": 1, "aaa": 1})
	wantKeys(t, got, "aaa", "zzz")
}

func TestDescend(t *testing.T) {
	tb := testTable()
	if got, ok := tb.Descend("Root", "objectType"); !ok || got != "ObjType" {
		t.Errorf("got %q %v", got, ok)
	}
	if got, ok := tb.Descend("Root"); !ok || got != "Root" {
		t.Errorf("got %q %v", got, ok)
	}
	for _, items := range [][]string{{"nope"}, {"objectType", "attribute"}, {"objectType", "nope"}, {"text"}} {
		if got, ok := tb.Descend("Root", items...); ok {
			t.Errorf("%v: got %q, want false", items, got)
		}
	}
	if _, ok := tb.Descend("Nope"); ok {
		t.Error("unknown root should be false")
	}
}

func TestLine(t *testing.T) {
	for in, want := range map[string]string{"4.9": "4.9", "4.9.3": "4.9", "4.9.3-SNAPSHOT": "4.9", "4.11.0": "4.11"} {
		if got, ok := line(in); !ok || got != want {
			t.Errorf("line(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc"} {
		if got, ok := line(in); ok {
			t.Errorf("line(%q) = %q, want false", in, got)
		}
	}
}

func TestFor_noTablesYet(t *testing.T) {
	if tb, ok := For("4.9.3"); ok || tb != nil {
		t.Errorf("For = %v, %v", tb, ok)
	}
}
