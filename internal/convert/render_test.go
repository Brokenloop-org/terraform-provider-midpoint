package convert

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"gopkg.in/yaml.v3"
)

var update = flag.Bool("update", false, "rewrite the golden files of TestRender")

const goldenDir = "testdata/golden"

// pipeline runs the whole conversion on the fixture catalogue.
func pipeline(t *testing.T) ([]Resource, []DataLookup) {
	t.Helper()
	out, data, _ := Rewrite(catalogue(t))
	return out, data
}

func render(t *testing.T, rs []Resource, data []DataLookup, o Options) map[string][]byte {
	t.Helper()
	files, err := Render(rs, data, o)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRender(t *testing.T) {
	t.Run("complete_catalogue_hcl", func(t *testing.T) {
		rs, data := pipeline(t)
		files := render(t, rs, data, Options{})
		want := []string{
			"archetypes.tf", "data.tf", "imports.tf", "object_templates.tf", "objects.tf", "orgs.tf",
			"marks.tf", "resources.tf", "roles.tf", "services.tf", "tasks.tf",
		}
		if got := sortedKeys(files); !reflect.DeepEqual(got, sorted(want)) {
			t.Errorf("files = %v, want %v", got, sorted(want))
		}
		golden(t, files)
		roundTrip(t, files, rs, data)
	})

	t.Run("json_format", func(t *testing.T) {
		rs, data := pipeline(t)
		files := render(t, rs, data, Options{Format: "json"})
		var want []string
		for name := range render(t, rs, data, Options{}) {
			want = append(want, name+".json")
		}
		if got := sortedKeys(files); !reflect.DeepEqual(got, sorted(want)) {
			t.Errorf("files = %v, want %v", got, sorted(want))
		}
		roundTrip(t, files, rs, data)

		// A free-form block attribute is plain JSON text without a Ref, a
		// jsonencode template with one.
		var orgs, services map[string]any
		mustUnmarshal(t, files["orgs.tf.json"], &orgs)
		mustUnmarshal(t, files["services.tf.json"], &services)
		platform := byOID(t, rs, platOrgOID)
		org := dig(orgs, "resource", "midpoint_org", platform.Label).(map[string]any)
		extra := org["assignment"].([]any)[1].(map[string]any)["extra"].(string)
		if !strings.HasPrefix(extra, "${jsonencode({ tenantRef = { oid = midpoint_org.") {
			t.Errorf("extra with a ref = %s", extra)
		}
		grafana := byOID(t, rs, "8c2e90b4-17d3-4a65-8f0b-d3a6e15c7b22")
		svc := dig(services, "resource", "midpoint_service", grafana.Label).(map[string]any)
		body := svc["inducement"].([]any)[0].(map[string]any)["construction"].(map[string]any)["body"].(string)
		if !strings.HasPrefix(body, `{"attribute":[`) || !strings.Contains(body, "$${basic.stringify(focus?.name)}") {
			t.Errorf("body without a ref = %s", body)
		}
		if got := dig(svc, "archetype"); got != "${midpoint_archetype.platform_item.oid}" {
			t.Errorf("archetype = %v", got)
		}
	})

	t.Run("split_files_resource", func(t *testing.T) {
		var attrs []any
		for i := range 12 {
			attrs = append(attrs, map[string]any{"ref": fmt.Sprintf("ri:attr%d", i), "outbound": map[string]any{"strength": "strong"}})
		}
		xsd := "<xsd:schema xmlns:xsd=\"http://www.w3.org/2001/XMLSchema\">\n  <xsd:complexType name=\"AccountObjectClass\"/>\n</xsd:schema>\n"
		res := Resource{Kind: "midpoint_resource", Label: "big", OID: resOID, Attrs: map[string]any{
			"name":                    "Big",
			"connector":               map[string]any{"type": "a.b.Csv"},
			"connector_configuration": map[string]any{"configurationProperties": map[string]any{"filePath": "/x.csv"}},
			"schema_handling": map[string]any{"objectType": []any{map[string]any{"kind": "account", "attribute": attrs, "n": json.Number("2.50"), "yes": "yes", "t": true,
				"<<": "merge", "on": "off", "1": "one", "=": "value", "plain": "text"}}},
			"synchronization": map[string]any{"objectSynchronization": attrsWithRef(25)},
			"schema":          map[string]any{"definition": xsd, "generation_constraints": map[string]any{"generateObjectClass": []any{"ri:AccountObjectClass"}}},
		}}
		target := Resource{Kind: "midpoint_role", Label: "target", OID: opRoleOID, Attrs: map[string]any{"name": "t"}}
		rs := []Resource{res, target}

		files := render(t, rs, nil, Options{SplitFiles: true})
		hclText := string(files["resources.tf"])
		for _, want := range []string{
			`schema_handling = yamldecode(file("${path.module}/big/schema_handling.yaml"))`,
			`definition = file("${path.module}/big/schema.xsd")`,
			`# synchronization holds a Terraform reference, so it stays here instead of in big/synchronization.yaml.`,
			"synchronization = {",
			"connector_configuration = {",
		} {
			if !strings.Contains(hclText, want) {
				t.Errorf("resources.tf lacks %q:\n%s", want, hclText)
			}
		}
		if string(files["big/schema.xsd"]) != xsd {
			t.Errorf("schema.xsd = %q", files["big/schema.xsd"])
		}
		if _, ok := files["big/connector_configuration.yaml"]; ok {
			t.Error("a small attribute was split")
		}
		y := string(files["big/schema_handling.yaml"])
		if !strings.HasPrefix(y, "objectType:\n  - \"1\": one\n") || !strings.Contains(y, "\"n\": 2.50\n") || !strings.Contains(y, `"yes": "yes"`) ||
			!strings.Contains(y, `"<<": merge`) || !strings.Contains(y, `"on": "off"`) || !strings.Contains(y, `"1": one`) ||
			!strings.Contains(y, `"=": value`) || !strings.Contains(y, "plain: text") {
			t.Errorf("schema_handling.yaml =\n%s", y)
		}
		roundTrip(t, files, rs, nil)

		files = render(t, rs, nil, Options{SplitFiles: true, Format: "json"})
		roundTrip(t, files, rs, nil)
		if !strings.Contains(string(files["resources.tf.json"]), `"schema_handling": "${yamldecode(file(\"${path.module}/big/schema_handling.yaml\"))}"`) {
			t.Errorf("resources.tf.json:\n%s", files["resources.tf.json"])
		}

		// Without SplitFiles everything stays inline.
		files = render(t, rs, nil, Options{})
		if len(files) != 3 || strings.Contains(string(files["resources.tf"]), "file(") {
			t.Errorf("files without split = %v", sortedKeys(files))
		}
	})

	t.Run("escapes_template_sequences", func(t *testing.T) {
		groovy := "// joins ${a} and %{b}\nif (x) {\n    return \"${givenName} ${familyName}\"\n}"
		values := map[string]any{
			"groovy_no_final_newline": groovy,
			"groovy_final_newline":    groovy + "\n",
			"all_indented":            "    a\n      b\n",
			"blank_lines":             "a\n\n   \n\tb\n\n",
			"delimiter_inside":        "EOT\n  EOT1\nx",
			"already_escaped":         "$${x} %%{y} $$${z}",
			"dollar_end":              "cost: 5$\n100%",
			"carriage_return":         "a\r\nb",
			"unicode":                 "naïve \u00e9t\u00e9 ☃\nsecond",
			"quotes_backslash":        `say "hi" \ ${x}`,
			"${key}":                  "%{if}",
			"for":                     "null",
			"list_of_scripts":         []any{"a\nb\n", "c ${d}\ne"},
			"mixed":                   []any{nil, json.Number("1e3"), 1.5, 7, int64(-2), true, map[string]any{}, []any{}},
		}
		esc := Resource{Kind: "midpoint_role", Label: "esc", OID: opRoleOID, Attrs: map[string]any{
			"name":        "esc ${x}",
			"description": "line ${one}\nline %{two}",
			"extension":   values,
		}, Blocks: []Block{{Type: "inducement", Attrs: map[string]any{
			"condition": map[string]any{"expression": map[string]any{"script": map[string]any{"code": groovy}}},
		}, Blocks: []Block{{Type: "construction", Attrs: map[string]any{
			"resource": Ref{Address: "midpoint_object.lib.oid"},
			"body":     map[string]any{"attribute": []any{map[string]any{"code": groovy, "ref": Ref{Address: "midpoint_object.lib.oid"}}}},
		}}}}}, Extra: map[string]any{"note": "${extra}"}}
		lib := Resource{Kind: "midpoint_object", Label: "lib", OID: "7e2b8f10-5a36-4d94-a0c7-3b69d1e48f77", Attrs: map[string]any{
			"type": "functionLibraries",
			"body": map[string]any{"name": "lib", "function": []any{map[string]any{"script": map[string]any{"code": groovy}}}},
		}}
		rs := []Resource{esc, lib}

		files := render(t, rs, nil, Options{})
		roles := string(files["roles.tf"])
		for _, want := range []string{
			`name  = "esc $${x}"`,
			"description = chomp(<<-EOT\n    line $${one}\n    line %%{two}\n  EOT\n  )\n",
			"      return \"$${givenName} $${familyName}\"\n",
			"= <<EOT\n    a\n      b\n    EOT\n",
			`"$${key}"       = "%%{if}"`,
			`carriage_return = "a\r\nb"`,
		} {
			if !strings.Contains(roles, want) {
				t.Errorf("roles.tf lacks %q", want)
			}
		}
		if !regexp.MustCompile(`\n\s*"for"\s+= "null"\n`).MatchString(roles) {
			t.Error(`roles.tf lacks the quoted key "for"`)
		}
		if t.Failed() {
			t.Logf("roles.tf:\n%s", roles)
		}
		roundTrip(t, files, rs, nil)
		roundTrip(t, render(t, rs, nil, Options{Format: "json"}), rs, nil)
	})

	t.Run("imports_file", func(t *testing.T) {
		rs := []Resource{
			{Kind: "midpoint_role", Label: "vault_operator", OID: opRoleOID, Attrs: map[string]any{"name": "vault / operator"}},
			{Kind: "midpoint_object", Label: "naming", OID: "7e2b8f10-5a36-4d94-a0c7-3b69d1e48f77",
				Attrs: map[string]any{"type": "functionLibraries", "body": map[string]any{"name": "Naming"}}},
			{Kind: "midpoint_service", Label: "new", Attrs: map[string]any{"name": "new"}},
			{Kind: "midpoint_archetype", Label: "application_role", OID: "00000000-0000-0000-0000-000000000328",
				Attrs: map[string]any{"name": "Application role"}, AdoptExisting: true},
		}
		files := render(t, rs, nil, Options{})
		want := `import {
  to = midpoint_archetype.application_role
  id = "00000000-0000-0000-0000-000000000328"
}

import {
  to = midpoint_object.naming
  id = "functionLibraries/7e2b8f10-5a36-4d94-a0c7-3b69d1e48f77"
}

import {
  to = midpoint_role.vault_operator
  id = "9a5bdeed-fa50-4a7d-bebc-604e5ddf0298"
}
`
		if got := string(files["imports.tf"]); got != want {
			t.Errorf("imports.tf =\n%s\nwant\n%s", got, want)
		}
		if _, ok := files["data.tf"]; ok {
			t.Error("data.tf written without lookups")
		}
		files = render(t, rs, nil, Options{Format: "json"})
		var doc struct {
			Import []map[string]string `json:"import"`
		}
		mustUnmarshal(t, files["imports.tf.json"], &doc)
		wantJSON := []map[string]string{
			{"to": "midpoint_archetype.application_role", "id": "00000000-0000-0000-0000-000000000328"},
			{"to": "midpoint_object.naming", "id": "functionLibraries/7e2b8f10-5a36-4d94-a0c7-3b69d1e48f77"},
			{"to": "midpoint_role.vault_operator", "id": opRoleOID},
		}
		if !reflect.DeepEqual(doc.Import, wantJSON) {
			t.Errorf("imports.tf.json = %v", doc.Import)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for name, rs := range map[string][]Resource{
			"unknown kind":     {{Kind: "midpoint_user", Label: "x"}},
			"object extra":     {{Kind: "midpoint_object", Label: "x", Attrs: map[string]any{"type": "users", "body": map[string]any{}}, Extra: map[string]any{"a": 1}}},
			"duplicate label":  {{Kind: "midpoint_role", Label: "x"}, {Kind: "midpoint_role", Label: "x"}},
			"unsupported type": {{Kind: "midpoint_role", Label: "x", Attrs: map[string]any{"extension": map[string]any{"a": struct{}{}}}}},
			"invalid utf-8":    {{Kind: "midpoint_role", Label: "x", Attrs: map[string]any{"name": "a\xffb"}}},
			"bad ref":          {{Kind: "midpoint_role", Label: "x", Attrs: map[string]any{"archetype": Ref{Address: "a b"}}}},
		} {
			if _, err := Render(rs, nil, Options{}); err == nil {
				t.Errorf("%s: no error", name)
			}
		}
		if _, err := Render(nil, nil, Options{Format: "yaml"}); err == nil {
			t.Error("unknown format: no error")
		}
	})
}

// attrsWithRef is a list of n maps, the first holding a Ref.
func attrsWithRef(n int) []any {
	out := []any{map[string]any{"objectTemplateRef": map[string]any{"oid": Ref{Address: "midpoint_role.target.oid"}}}}
	for i := 1; i < n; i++ {
		out = append(out, map[string]any{"name": fmt.Sprintf("s%d", i)})
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func mustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func dig(v any, path ...string) any {
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	return v
}

// golden compares files with the golden directory, or rewrites it with
// -update.
func golden(t *testing.T, files map[string][]byte) {
	t.Helper()
	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			p := filepath.Join(goldenDir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	var onDisk []string
	err := filepath.WalkDir(goldenDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(goldenDir, p)
		onDisk = append(onDisk, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(files); !reflect.DeepEqual(got, sorted(onDisk)) {
		t.Errorf("golden files = %v, rendered %v (run with -update)", sorted(onDisk), got)
	}
	for name, content := range files {
		want, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			continue
		}
		if string(want) != string(content) {
			t.Errorf("%s differs from the golden file (run with -update):\n%s", name, content)
		}
	}
}

// roundTrip parses the rendered configuration and evaluates every
// attribute of every resource, data source and import, checking that it
// yields exactly the model value: strings byte for byte, block free-form
// attributes as JSON text of the same value, a Ref as the value of the
// traversal it names.
func roundTrip(t *testing.T, files map[string][]byte, rs []Resource, data []DataLookup) {
	t.Helper()
	ctx := evalContext(rs, data, files)
	byAddr := map[string]Resource{}
	for _, r := range rs {
		byAddr[r.Kind+"."+r.Label] = r
	}
	seen := map[string]bool{}
	parser := hclparse.NewParser()
	top := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "resource", LabelNames: []string{"type", "name"}},
		{Type: "data", LabelNames: []string{"type", "name"}},
		{Type: "import"},
	}}
	for _, name := range sortedKeys(files) {
		var f *hcl.File
		var diags hcl.Diagnostics
		switch {
		case strings.HasSuffix(name, ".tf"):
			f, diags = parser.ParseHCL(files[name], name)
		case strings.HasSuffix(name, ".tf.json"):
			f, diags = parser.ParseJSON(files[name], name)
		default:
			continue
		}
		if diags.HasErrors() {
			t.Fatalf("%s: %s", name, diags.Error())
		}
		content, diags := f.Body.Content(top)
		if diags.HasErrors() {
			t.Fatalf("%s: %s", name, diags.Error())
		}
		for _, b := range content.Blocks {
			switch b.Type {
			case "resource":
				addr := b.Labels[0] + "." + b.Labels[1]
				r, ok := byAddr[addr]
				if !ok {
					t.Errorf("%s: unexpected resource %s", name, addr)
					continue
				}
				seen[addr] = true
				want := cloneMap(r.Attrs)
				if r.OID != "" {
					want["oid"] = r.OID
				}
				if r.AdoptExisting {
					want["adopt_existing"] = true
				}
				if len(r.Extra) > 0 {
					want["extra"] = r.Extra
				}
				checkBody(t, addr, b.Body, want, r.Blocks, false, ctx)
			case "data":
				i := slices.IndexFunc(data, func(d DataLookup) bool { return d.Label == b.Labels[1] })
				if b.Labels[0] != "midpoint_object" || i < 0 {
					t.Errorf("%s: unexpected data %v", name, b.Labels)
					continue
				}
				seen["data."+data[i].Label] = true
				checkBody(t, "data."+data[i].Label, b.Body, map[string]any{"type": data[i].RestType, "oid": data[i].OID}, nil, false, ctx)
			case "import":
				attrs, diags := b.Body.JustAttributes()
				if diags.HasErrors() {
					t.Fatalf("%s: %s", name, diags.Error())
				}
				to, diags := hcl.AbsTraversalForExpr(attrs["to"].Expr)
				if diags.HasErrors() {
					t.Fatalf("%s: import to: %s", name, diags.Error())
				}
				addr := to.RootName() + "." + to[1].(hcl.TraverseAttr).Name
				r := byAddr[addr]
				wantID := r.OID
				if r.Kind == "midpoint_object" {
					wantID = r.Attrs["type"].(string) + "/" + r.OID
				}
				id, _ := attrs["id"].Expr.Value(nil)
				if r.OID == "" || id.AsString() != wantID {
					t.Errorf("import %s id = %#v, want %q", addr, id, wantID)
				}
				seen["import."+addr] = true
			}
		}
	}
	for _, r := range rs {
		addr := r.Kind + "." + r.Label
		if !seen[addr] {
			t.Errorf("resource %s not rendered", addr)
		}
		if r.OID != "" && !seen["import."+addr] {
			t.Errorf("no import for %s", addr)
		}
	}
	for _, d := range data {
		if !seen["data."+d.Label] {
			t.Errorf("data %s not rendered", d.Label)
		}
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// checkBody checks that body holds exactly the attributes want and the
// blocks blocks.
func checkBody(t *testing.T, where string, body hcl.Body, want map[string]any, blocks []Block, inBlock bool, ctx *hcl.EvalContext) {
	t.Helper()
	schema := &hcl.BodySchema{}
	for k := range want {
		schema.Attributes = append(schema.Attributes, hcl.AttributeSchema{Name: k})
	}
	types := map[string]bool{}
	for _, b := range blocks {
		if !types[b.Type] {
			types[b.Type] = true
			schema.Blocks = append(schema.Blocks, hcl.BlockHeaderSchema{Type: b.Type})
		}
	}
	content, diags := body.Content(schema)
	if diags.HasErrors() {
		t.Errorf("%s: %s", where, diags.Error())
		return
	}
	for k, w := range want {
		a, ok := content.Attributes[k]
		if !ok {
			t.Errorf("%s: attribute %s missing", where, k)
			continue
		}
		v, diags := a.Expr.Value(ctx)
		if diags.HasErrors() {
			t.Errorf("%s.%s: %s", where, k, diags.Error())
			continue
		}
		got := ctyToGo(t, v)
		if inBlock && isFreeForm(w) {
			s, ok := got.(string)
			if !ok {
				t.Errorf("%s.%s: free-form block attribute is %T, want a JSON string", where, k, got)
				continue
			}
			got = nil
			if err := json.Unmarshal([]byte(s), &got); err != nil {
				t.Errorf("%s.%s: %v", where, k, err)
				continue
			}
		}
		if exp := expected(w); !reflect.DeepEqual(got, exp) {
			g, _ := json.Marshal(got)
			e, _ := json.Marshal(exp)
			t.Errorf("%s.%s:\n got %s\nwant %s", where, k, g, e)
		}
	}
	if len(content.Blocks) != len(blocks) {
		t.Errorf("%s: %d blocks, want %d", where, len(content.Blocks), len(blocks))
		return
	}
	for i, b := range content.Blocks {
		if b.Type != blocks[i].Type {
			t.Errorf("%s: block %d is %s, want %s", where, i, b.Type, blocks[i].Type)
			continue
		}
		checkBody(t, fmt.Sprintf("%s.%s[%d]", where, b.Type, i), b.Body, blocks[i].Attrs, blocks[i].Blocks, true, ctx)
	}
}

// expected is the evaluated form of a model value: a Ref is the string its
// traversal holds in the test context, numbers are float64.
func expected(v any) any {
	var conv func(v any) any
	conv = func(v any) any {
		switch t := v.(type) {
		case Ref:
			return "ref:" + t.Address
		case map[string]any:
			out := map[string]any{}
			for k, e := range t {
				out[k] = conv(e)
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = conv(e)
			}
			return out
		default:
			return v
		}
	}
	data, err := json.Marshal(conv(v))
	if err != nil {
		panic(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}

func ctyToGo(t *testing.T, v cty.Value) any {
	t.Helper()
	data, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// evalContext resolves every resource and data oid to "ref:<address>",
// path.module to "MODULE", and provides the functions the renderer uses;
// file reads the rendered files.
func evalContext(rs []Resource, data []DataLookup, files map[string][]byte) *hcl.EvalContext {
	kinds := map[string]map[string]cty.Value{}
	for _, r := range rs {
		if kinds[r.Kind] == nil {
			kinds[r.Kind] = map[string]cty.Value{}
		}
		kinds[r.Kind][r.Label] = cty.ObjectVal(map[string]cty.Value{"oid": cty.StringVal("ref:" + r.Kind + "." + r.Label + ".oid")})
	}
	vars := map[string]cty.Value{"path": cty.ObjectVal(map[string]cty.Value{"module": cty.StringVal("MODULE")})}
	for k, labels := range kinds {
		vars[k] = cty.ObjectVal(labels)
	}
	lookups := map[string]cty.Value{}
	for _, d := range data {
		lookups[d.Label] = cty.ObjectVal(map[string]cty.Value{"oid": cty.StringVal("ref:" + d.Address())})
	}
	if len(lookups) > 0 {
		vars["data"] = cty.ObjectVal(map[string]cty.Value{"midpoint_object": cty.ObjectVal(lookups)})
	}
	file := function.New(&function.Spec{
		Params: []function.Parameter{{Name: "path", Type: cty.String}},
		Type:   function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			rel, ok := strings.CutPrefix(args[0].AsString(), "MODULE/")
			content, found := files[rel]
			if !ok || !found {
				return cty.NilVal, fmt.Errorf("no file %s", args[0].AsString())
			}
			return cty.StringVal(string(content)), nil
		},
	})
	yamldecode := function.New(&function.Spec{
		Params: []function.Parameter{{Name: "src", Type: cty.String}},
		Type: func(args []cty.Value) (cty.Type, error) {
			v, err := ctyFromYAML(args[0].AsString())
			return v.Type(), err
		},
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) { return ctyFromYAML(args[0].AsString()) },
	})
	return &hcl.EvalContext{Variables: vars, Functions: map[string]function.Function{
		"jsonencode": stdlib.JSONEncodeFunc, "chomp": stdlib.ChompFunc, "file": file, "yamldecode": yamldecode,
	}}
}

func ctyFromYAML(src string) (cty.Value, error) {
	var v any
	if err := yaml.Unmarshal([]byte(src), &v); err != nil {
		return cty.NilVal, err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return cty.NilVal, err
	}
	typ, err := ctyjson.ImpliedType(data)
	if err != nil {
		return cty.NilVal, err
	}
	return ctyjson.Unmarshal(data, typ)
}
