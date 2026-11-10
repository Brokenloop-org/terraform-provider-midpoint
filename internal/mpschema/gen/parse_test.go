package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpschema"
)

// build copies the named testdata files to a temporary root and parses them.
func build(t *testing.T, files ...string) *mpschema.Table {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		copyFile(t, filepath.Join("testdata", f), filepath.Join(dir, f))
	}
	tb, err := parseDir(dir)
	if err != nil {
		t.Fatalf("parseDir: %v", err)
	}
	return tb
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func typeOf(t *testing.T, tb *mpschema.Table, name string) *mpschema.Type {
	t.Helper()
	ty, ok := tb.Types[name]
	if !ok {
		t.Fatalf("type %s missing; have %v", name, typeNames(tb))
	}
	return ty
}

func typeNames(tb *mpschema.Table) []string {
	names := make([]string, 0, len(tb.Types))
	for n := range tb.Types {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func wantItem(t *testing.T, tb *mpschema.Table, typeName, key string, want mpschema.Item) {
	t.Helper()
	got, ok := typeOf(t, tb, typeName).Items[key]
	if !ok {
		t.Errorf("%s.%s missing", typeName, key)
		return
	}
	if got != want {
		t.Errorf("%s.%s = %+v, want %+v", typeName, key, got, want)
	}
}

func wantOpen(t *testing.T, tb *mpschema.Table, typeName string, want bool) {
	t.Helper()
	if got := typeOf(t, tb, typeName).Open; got != want {
		t.Errorf("%s.Open = %v, want %v", typeName, got, want)
	}
}

var (
	simple  = mpschema.Item{Kind: mpschema.KindSimple}
	multi   = mpschema.Item{Kind: mpschema.KindSimple, Multi: true}
	open    = mpschema.Item{Kind: mpschema.KindOpen}
	integer = mpschema.Item{Kind: mpschema.KindInteger}
	boolean = mpschema.Item{Kind: mpschema.KindBoolean}
)

func TestParse_inheritsThroughTwoLevels(t *testing.T) {
	tb := build(t, "inherit.xsd")
	for _, k := range []string{"baseItem", "middleItem", "leafItem", "oid"} {
		wantItem(t, tb, "Leaf", k, simple)
	}
	if _, ok := typeOf(t, tb, "Middle").Items["leafItem"]; ok {
		t.Error("a key leaked from a derived type to its base")
	}
}

func TestParse_attributesAreKeys(t *testing.T) {
	tb := build(t, "attributes.xsd")
	for _, k := range []string{"oid", "type", "relation"} {
		wantItem(t, tb, "Ref", k, simple)
	}
	wantItem(t, tb, "Ref", "id", integer)
}

func TestParse_substitution(t *testing.T) {
	tb := build(t, "substitution.xsd")
	wantItem(t, tb, "Many", "evaluator", mpschema.Item{Type: "EvaluatorBase", Kind: mpschema.KindComplex, Multi: true})
	wantItem(t, tb, "Many", "const", mpschema.Item{Type: "ConstEval", Kind: mpschema.KindComplex, Multi: true})
	wantItem(t, tb, "Many", "nested", mpschema.Item{Type: "NestedEval", Kind: mpschema.KindComplex, Multi: true})
	wantItem(t, tb, "One", "const", mpschema.Item{Type: "ConstEval", Kind: mpschema.KindComplex})
	wantItem(t, tb, "One", "nested", mpschema.Item{Type: "NestedEval", Kind: mpschema.KindComplex})
	if _, ok := typeOf(t, tb, "SameLocalNameNotARef").Items["const"]; ok {
		t.Error("a local element sharing a head's name must not take its members")
	}
}

func TestParse_substitutionInheritsEnclosingUnbounded(t *testing.T) {
	tb := build(t, "multi.xsd")
	wantItem(t, tb, "SeqAround", "head", multi)
	wantItem(t, tb, "SeqAround", "member", multi)
	wantItem(t, tb, "Occurs", "head", multi)
	wantItem(t, tb, "Occurs", "member", multi)
}

func TestParse_substitutionResolvedByNamespace(t *testing.T) {
	tb := build(t, "collision_script_core.xsd", "collision_script_caps.xsd")
	wantItem(t, tb, "Expression", "script", mpschema.Item{Type: "ScriptEval", Kind: mpschema.KindComplex, Multi: true})
	wantItem(t, tb, "Capabilities", "script", mpschema.Item{Type: "ScriptCap", Kind: mpschema.KindComplex})
}

func TestParse_elementsTable(t *testing.T) {
	tb := build(t, "collision_script_core.xsd", "collision_script_caps.xsd", "kinds.xsd", "anonymous.xsd")
	if got := tb.Elements["role"]; got != "ScriptEval" {
		t.Errorf("Elements[role] = %q", got)
	}
	if got := tb.Elements["evaluator"]; got != "EvaluatorBase" {
		t.Errorf("Elements[evaluator] = %q", got)
	}
	if got, ok := tb.Elements["script"]; ok {
		t.Errorf("Elements[script] = %q: two namespaces give it different types, so it must be absent", got)
	}
	if got := tb.Elements["globalAnon"]; got == "" || tb.Types[got] == nil {
		t.Errorf("Elements[globalAnon] = %q, want a type in the table", got)
	}
}

func TestParse_typeCollision(t *testing.T) {
	tb := build(t, "collision_type_a.xsd", "collision_type_b.xsd")
	wantOpen(t, tb, "Shared", true)
	wantOpen(t, tb, "Same", false)
	wantItem(t, tb, "Same", "x", simple)
	// The child's own keys are still right: its base is resolved by namespace.
	wantOpen(t, tb, "ChildOfShared", false)
	wantItem(t, tb, "ChildOfShared", "fromA", simple)
	wantItem(t, tb, "ChildOfShared", "own", simple)
	// A complex type sharing its name with a simple type elsewhere.
	wantOpen(t, tb, "Both", true)
}

func TestParse_enumCollision(t *testing.T) {
	tb := build(t, "collision_type_a.xsd", "collision_type_b.xsd")
	if v, ok := tb.Enums["Color"]; ok {
		t.Errorf("Enums[Color] = %v: the namespaces disagree, so it must be absent", v)
	}
	wantItem(t, tb, "UsesColor", "color", simple)
	wantItem(t, tb, "UsesColor", "same", mpschema.Item{Type: "SameEnum", Kind: mpschema.KindEnum})
	if !reflect.DeepEqual(tb.Enums["SameEnum"], []string{"a", "b"}) {
		t.Errorf("Enums[SameEnum] = %v", tb.Enums["SameEnum"])
	}
}

func TestParse_openTypes(t *testing.T) {
	tb := build(t, "open.xsd")
	for name, want := range map[string]bool{
		"WithAny":           true,
		"WithAnyAttribute":  true,
		"WithRawAnnotation": true,
		"ChildOfAny":        true,
		"SearchFilterType":  true,
		"ConditionalFilter": true,
		"RawType":           true,
		"SimpleContent":     true,
		"Restricted":        true,
		"UnknownBase":       true,
		"Plain":             false,
	} {
		wantOpen(t, tb, name, want)
	}
	wantItem(t, tb, "HasRawItem", "raw", open)
}

func TestParse_anonymousTypes(t *testing.T) {
	tb := build(t, "anonymous.xsd")
	wantItem(t, tb, "Owner", "inner", mpschema.Item{Type: "Owner.inner", Kind: mpschema.KindComplex, Multi: true})
	wantItem(t, tb, "Owner.inner", "leaf", simple)
	wantItem(t, tb, "Owner.inner", "deep", mpschema.Item{Type: "Owner.inner.deep", Kind: mpschema.KindComplex})
	wantItem(t, tb, "Owner.inner.deep", "x", simple)
	wantItem(t, tb, "Owner", "mode", mpschema.Item{Type: "Owner.mode", Kind: mpschema.KindEnum})
	if !reflect.DeepEqual(tb.Enums["Owner.mode"], []string{"on", "off"}) {
		t.Errorf("Enums[Owner.mode] = %v", tb.Enums["Owner.mode"])
	}
}

func TestParse_groupsFlattened(t *testing.T) {
	tb := build(t, "groups.xsd")
	wantItem(t, tb, "UsesGroups", "left", simple)
	wantItem(t, tb, "UsesGroups", "right", multi)
	wantItem(t, tb, "UsesGroups", "groupAttr", boolean)
	wantItem(t, tb, "UsesGroups", "deepAttr", simple)
	wantItem(t, tb, "RepeatedGroup", "left", multi)
	wantOpen(t, tb, "UnknownGroup", true)
}

func TestParse_multi(t *testing.T) {
	tb := build(t, "multi.xsd")
	for key, want := range map[string]mpschema.Item{
		"single":        simple,
		"explicitOne":   simple,
		"two":           multi,
		"many":          multi,
		"inSeq":         multi,
		"inChoice":      multi,
		"deepInChoice":  multi,
		"inPlainChoice": simple,
		"annotatedMany": multi,
		"annotatedOne":  simple,
		// Declared twice in one content model, so more than one can occur.
		"twice": multi,
		// Declared twice with different types: nothing can be said of it.
		"conflict": {Kind: mpschema.KindOpen, Multi: true},
	} {
		wantItem(t, tb, "Occurs", key, want)
	}
	wantItem(t, tb, "ChildOfOccurs", "many", multi)
	wantItem(t, tb, "ChildOfOccurs", "single", simple)
}

func TestParse_kinds(t *testing.T) {
	tb := build(t, "kinds.xsd")
	strength := mpschema.Item{Type: "Strength", Kind: mpschema.KindEnum}
	for key, want := range map[string]mpschema.Item{
		"b":              boolean,
		"i1":             integer,
		"i2":             integer,
		"i3":             integer,
		"i4":             integer,
		"i5":             integer,
		"i6":             integer,
		"i7":             integer,
		"i8":             integer,
		"dbl":            simple,
		"str":            simple,
		"when":           simple,
		"qn":             simple,
		"anything":       open,
		"noType":         open,
		"badRef":         open,
		"missingElement": open,
		"strength":       strength,
		"alias":          {Type: "AliasOfAlias", Kind: mpschema.KindEnum},
		"weak":           {Type: "Weak", Kind: mpschema.KindEnum},
		"port":           integer,
		"flag":           boolean,
		"text":           simple,
		"either":         simple,
		"words":          simple,
		"attrFlag":       boolean,
		"attrStrength":   strength,
	} {
		wantItem(t, tb, "Kinds", key, want)
	}
	if !reflect.DeepEqual(tb.Enums["Strength"], []string{"strong", "weak"}) {
		t.Errorf("Enums[Strength] = %v", tb.Enums["Strength"])
	}
	if !reflect.DeepEqual(tb.Enums["AliasOfAlias"], []string{"strong", "weak"}) {
		t.Errorf("Enums[AliasOfAlias] = %v", tb.Enums["AliasOfAlias"])
	}
	if !reflect.DeepEqual(tb.Enums["Weak"], []string{"weak"}) {
		t.Errorf("Enums[Weak] = %v", tb.Enums["Weak"])
	}
}

func TestParse_prefixesComeFromEachFileAndScope(t *testing.T) {
	tb := build(t, "prefixes.xsd")
	wantItem(t, tb, "DefaultPrefix", "n", integer)
	wantItem(t, tb, "DefaultPrefix", "scoped", mpschema.Item{Type: "DefaultPrefix", Kind: mpschema.KindComplex})
}

func TestParse_skipsTestAndDuplicatePaths(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/inherit.xsd", filepath.Join(dir, "xml/ns/public/inherit.xsd"))
	copyFile(t, "testdata/skipped.xsd", filepath.Join(dir, "xml/ns/test/foo-1.xsd"))
	copyFile(t, "testdata/skipped.xsd", filepath.Join(dir, "prism-impl/xml/ns/public/types-3.xsd"))
	tb, err := parseDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tb.Types["Leaf"]; !ok {
		t.Error("the file outside the skipped paths was not read")
	}
	if _, ok := tb.Types["Skipped"]; ok {
		t.Error("a file under a skipped path was read")
	}
}

func TestParse_failsOnNoSchemasAndBadXML(t *testing.T) {
	if _, err := parseDir(t.TempDir()); err == nil {
		t.Error("an empty directory must fail: it would write an empty table")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.xsd"), []byte("<xsd:schema"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseDir(dir); err == nil {
		t.Error("malformed XML must fail")
	}
}

func TestGenerate_deterministic(t *testing.T) {
	dir := t.TempDir()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		copyFile(t, filepath.Join("testdata", e.Name()), filepath.Join(dir, e.Name()))
	}
	first, err := generate(dir, "4.9", "img@sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	second, err := generate(dir, "4.9", "img@sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two runs gave different bytes")
	}
	zr, err := gzip.NewReader(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if !zr.ModTime.IsZero() {
		t.Errorf("gzip mod time = %v, want zero", zr.ModTime)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var got mpschema.Table
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Line != "4.9" || got.Source != "img@sha256:abc" || len(got.Types) == 0 {
		t.Errorf("round trip: line %q source %q, %d types", got.Line, got.Source, len(got.Types))
	}
}

func TestParse_objectElementWinsNameCollision(t *testing.T) {
	tb := build(t, "elements_common.xsd", "elements_query.xsd")
	for name, want := range map[string]string{"object": "ObjectType", "org": "OrgT", "nested": "NestedT"} {
		if got := tb.Elements[name]; got != want {
			t.Errorf("Elements[%s] = %q, want %q", name, got, want)
		}
	}
	// Not an object, though in the common namespace: the rule stays cautious.
	if got, ok := tb.Elements["plain"]; ok {
		t.Errorf("Elements[plain] = %q, want absent", got)
	}
	if got := tb.Elements["clause"]; got != "Clause" {
		t.Errorf("Elements[clause] = %q", got)
	}
}

// Prism takes a primitive "id" as the container id when the definition has no
// "id" item, so it is valid whether or not the XSD declares it.
func TestParse_everyTypeTakesAnID(t *testing.T) {
	tb := build(t, "ownid.xsd", "inherit.xsd")
	wantItem(t, tb, "NoId", "id", integer)
	wantItem(t, tb, "Leaf", "id", integer)
	wantItem(t, tb, "ChildDeclaresId", "id", integer)
	wantItem(t, tb, "TextId", "id", simple)
}
