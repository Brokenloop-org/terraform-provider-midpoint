package convert

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	oidVault     = "3f6a1c52-8d0e-4b7a-9a41-5c2e7d9b0a11"
	oidTenant    = "1a9c5d70-3e84-4b26-9f0d-8c71b4a2e655"
	oidBuiltin   = "00000000-0000-0000-0000-000000000328"
	oidResource  = "25e166d6-9899-43b7-b0d7-4c4c526b7a9e"
	oidFunctions = "7e2b8f10-5a36-4d94-a0c7-3b69d1e48f77"
)

func bySourceOID(t *testing.T, srcs []Source) map[string]Source {
	t.Helper()
	m := map[string]Source{}
	for _, s := range srcs {
		if _, dup := m[s.OID]; dup {
			t.Fatalf("OID %s loaded twice", s.OID)
		}
		m[s.OID] = s
	}
	return m
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noteFor(notes []Note, pathSuffix, contains string) bool {
	for _, n := range notes {
		if strings.HasSuffix(filepath.ToSlash(n.Path), pathSuffix) && strings.Contains(n.Message, contains) {
			return true
		}
	}
	return false
}

func TestLoad(t *testing.T) {
	root := filepath.Join("testdata", "catalogue")

	t.Run("json tree", func(t *testing.T) {
		srcs, _, err := Load([]string{root})
		if err != nil {
			t.Fatal(err)
		}
		got := bySourceOID(t, srcs)
		// 2 services, 4 roles, 2 orgs, 3 archetypes, 1 template, 1 mark,
		// 1 task, 1 resource, 1 function library; the tenant file is a duplicate.
		if len(srcs) != 16 {
			t.Fatalf("loaded %d sources, want 16", len(srcs))
		}
		cases := []struct{ oid, restType, name string }{
			{oidVault, "services", "Vault"},
			{"9a5bdeed-fa50-4a7d-bebc-604e5ddf0298", "roles", "vault / operator"},
			{oidTenant, "orgs", "Acme Corp"},
			{oidBuiltin, "archetypes", "Application role"},
			{"a395aa95-3aea-4db3-893c-516cf8ca422b", "objectTemplates", "Person Object Template"},
			{"26d8fa29-0eb4-412b-93bb-da9ecfbb8a1a", "marks", "Missing from CSV - review"},
			{"214ed436-8d6e-4a57-b63b-f87bb8305a34", "tasks", "Users CSV reconciliation"},
			{oidResource, "resources", "Users CSV"},
			{oidFunctions, "functionLibraries", "Naming functions"},
		}
		for _, c := range cases {
			s, ok := got[c.oid]
			if !ok {
				t.Errorf("%s (%s) not loaded", c.oid, c.restType)
				continue
			}
			if s.RestType != c.restType || s.Name != c.name {
				t.Errorf("%s: RestType=%q Name=%q, want %q %q", c.oid, s.RestType, s.Name, c.restType, c.name)
			}
			if s.Body["oid"] != c.oid || s.Path == "" {
				t.Errorf("%s: body oid %v, path %q", c.oid, s.Body["oid"], s.Path)
			}
		}
		// Canonical form: a single inducement is a list, prefixes are gone.
		ind, ok := got["9a5bdeed-fa50-4a7d-bebc-604e5ddf0298"].Body["inducement"].([]any)
		if !ok || len(ind) != 1 {
			t.Errorf("inducement = %#v, want a one-element list", got["9a5bdeed-fa50-4a7d-bebc-604e5ddf0298"].Body["inducement"])
		}
	})

	t.Run("dedupes identical oid", func(t *testing.T) {
		srcs, notes, err := Load([]string{root})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, s := range srcs {
			if s.OID == oidTenant {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("tenant OID loaded %d times, want 1", n)
		}
		if !noteFor(notes, "tenants/"+oidTenant+".json", "duplicate") {
			t.Errorf("no duplicate note for the tenants copy: %v", notes)
		}
	})

	t.Run("conflicting oid errors", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "a", "x.json"), `{"role":{"oid":"11111111-1111-1111-1111-111111111111","name":"A"}}`)
		writeFile(t, filepath.Join(dir, "b", "y.json"), `{"role":{"oid":"11111111-1111-1111-1111-111111111111","name":"B"}}`)
		_, _, err := Load([]string{dir})
		var ce *ConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *ConflictError", err)
		}
		if ce.OID != "11111111-1111-1111-1111-111111111111" ||
			!strings.HasSuffix(ce.PathA, filepath.Join("a", "x.json")) || !strings.HasSuffix(ce.PathB, filepath.Join("b", "y.json")) {
			t.Errorf("conflict = %+v", ce)
		}
		if !strings.Contains(err.Error(), ce.PathA) || !strings.Contains(err.Error(), ce.PathB) {
			t.Errorf("message %q does not name both files", err)
		}
	})

	t.Run("skips non object files", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "ok.json"), `{"role":{"oid":"22222222-2222-2222-2222-222222222222","name":"ok"}}`)
		writeFile(t, filepath.Join(dir, "tpl.tmpl.json"), `{"role":{"oid":"${oid}"}}`)
		writeFile(t, filepath.Join(dir, "unknown.json"), `{"widget":{"oid":"33333333-3333-3333-3333-333333333333"}}`)
		writeFile(t, filepath.Join(dir, "two.json"), `{"role":{},"org":{}}`)
		writeFile(t, filepath.Join(dir, "README.md"), `# hi`)
		srcs, notes, err := Load([]string{dir})
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 1 || srcs[0].OID != "22222222-2222-2222-2222-222222222222" {
			t.Fatalf("sources = %+v, want only ok.json", srcs)
		}
		for _, f := range []string{"tpl.tmpl.json", "unknown.json", "two.json", "README.md"} {
			if !noteFor(notes, f, "skipped") {
				t.Errorf("no skip note for %s: %v", f, notes)
			}
		}
	})

	t.Run("malformed object file errors", func(t *testing.T) {
		for _, name := range []string{"broken.json", "broken.yaml", "broken.xml"} {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, name), "{not: [valid")
			_, _, err := Load([]string{dir})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s: err = %v, want an error naming the file", name, err)
			}
		}
	})

	t.Run("yaml input", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "r.yaml"), `role:
  oid: 44444444-4444-4444-4444-444444444444
  name: yaml role
  riskLevel: 3
  requestable: true
  inducement:
    targetRef:
      oid: 55555555-5555-5555-5555-555555555555
      type: c:ServiceType
`)
		writeFile(t, filepath.Join(dir, "r.json"), `{"role":{"oid":"44444444-4444-4444-4444-444444444444","name":"yaml role","riskLevel":3,"requestable":true,
"inducement":{"targetRef":{"oid":"55555555-5555-5555-5555-555555555555","type":"c:ServiceType"}}}}`)
		srcs, _, err := Load([]string{dir})
		if err != nil {
			t.Fatalf("yaml and json of the same object must dedupe: %v", err)
		}
		if len(srcs) != 1 || srcs[0].RestType != "roles" || srcs[0].Name != "yaml role" {
			t.Fatalf("sources = %+v", srcs)
		}
	})

	t.Run("xml input", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "r.xml"), `<?xml version="1.0"?>
<role xmlns="http://midpoint.evolveum.com/xml/ns/public/common/common-3" oid="66666666-6666-6666-6666-666666666666">
  <name>xml role</name>
  <description xsi:type="xsd:string" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">typed text</description>
  <activation/>
  <inducement id="1"><targetRef oid="77777777-7777-7777-7777-777777777777" type="c:ServiceType"/></inducement>
  <inducement id="2"><targetRef oid="88888888-8888-8888-8888-888888888888" type="c:ServiceType"/></inducement>
</role>`)
		srcs, _, err := Load([]string{dir})
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 1 || srcs[0].OID != "66666666-6666-6666-6666-666666666666" || srcs[0].Name != "xml role" {
			t.Fatalf("sources = %+v", srcs)
		}
		ind, ok := srcs[0].Body["inducement"].([]any)
		if !ok || len(ind) != 2 {
			t.Fatalf("inducement = %#v, want 2 values", srcs[0].Body["inducement"])
		}
		if srcs[0].Body["description"] != "typed text" {
			t.Errorf("description = %#v, want the text without xsi:type", srcs[0].Body["description"])
		}
		if act, ok := srcs[0].Body["activation"].(map[string]any); !ok || len(act) != 0 {
			t.Errorf("activation = %#v, want {} for an empty element", srcs[0].Body["activation"])
		}
		first := ind[0].(map[string]any)
		if first["@id"] != "1" {
			t.Errorf("first inducement = %#v, want @id 1", first)
		}
	})

	t.Run("xml text with attributes errors", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "r.xml"), `<role oid="99999999-9999-9999-9999-999999999999"><name lang="en">x</name></role>`)
		if _, _, err := Load([]string{dir}); err == nil {
			t.Fatal("want an error for text mixed with attributes")
		}
	})

	t.Run("records dropped server managed items", func(t *testing.T) {
		srcs, notes, err := Load([]string{root})
		if err != nil {
			t.Fatal(err)
		}
		got := bySourceOID(t, srcs)
		res := got[oidResource].Body
		if caps, _ := res["capabilities"].(map[string]any); caps != nil {
			if _, has := caps["native"]; has {
				t.Errorf("capabilities/native kept in %v", caps)
			}
		}
		if !noteFor(notes, oidResource+".json", "capabilities/native") {
			t.Errorf("no drop note naming capabilities/native: %v", notes)
		}
		if !noteFor(notes, "roles/service_roles/c41d7e03-6b52-4f19-a8d4-0e9b3a7f5c33.json", "roleMembershipRef") ||
			!noteFor(notes, "roles/service_roles/c41d7e03-6b52-4f19-a8d4-0e9b3a7f5c33.json", "@metadata") {
			t.Errorf("service role drop note incomplete: %v", notes)
		}
		if !noteFor(notes, "services/8c2e90b4-17d3-4a65-8f0b-d3a6e15c7b22.json", "metadata") {
			t.Errorf("no metadata drop note: %v", notes)
		}
		for _, n := range notes {
			if strings.HasSuffix(n.Path, oidVault+".json") {
				t.Errorf("unexpected note for a clean file: %+v", n)
			}
		}
	})
}

func TestRestTypesWrapperKeys(t *testing.T) {
	// Wrapper keys written out independently of client.SingularOf.
	want := map[string]string{
		"accessCertificationCampaigns": "accessCertificationCampaign", "accessCertificationDefinitions": "accessCertificationDefinition",
		"archetypes": "archetype", "connectorHosts": "connectorHost", "connectors": "connector", "dashboards": "dashboard",
		"forms": "form", "functionLibraries": "functionLibrary", "genericObjects": "genericObject", "lookupTables": "lookupTable",
		"marks": "mark", "nodes": "node", "objectCollections": "objectCollection", "objectTemplates": "objectTemplate",
		"orgs": "org", "policies": "policy", "reports": "report", "resources": "resource", "roles": "role",
		"securityPolicies": "securityPolicy", "services": "service", "shadows": "shadow", "systemConfigurations": "systemConfiguration",
		"tasks": "task", "users": "user", "valuePolicies": "valuePolicy",
	}
	if len(restTypes) != len(want) {
		t.Errorf("restTypes has %d entries, table has %d", len(restTypes), len(want))
	}
	for _, rt := range restTypes {
		key, ok := want[rt]
		if !ok {
			t.Errorf("%s missing from the table", rt)
			continue
		}
		if wrapperTypes[key] != rt {
			t.Errorf("wrapper key %q maps to %q, want %q", key, wrapperTypes[key], rt)
		}
	}
}
