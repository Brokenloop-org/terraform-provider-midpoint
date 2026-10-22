package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/jplana/terraform-provider-midpoint/internal/mpstub"
)

const (
	dsArchetypeOID = "00000000-0000-0000-0000-000000000329"
	dsObjectAddr   = "data.midpoint_object.test"
)

// putApplicationArchetype stores an archetype like midPoint's built-in
// Application archetype, with a container whose "@id" must not reach body.
func putApplicationArchetype(s *mpstub.Server) {
	s.Put("archetypes", map[string]any{
		"oid":         dsArchetypeOID,
		"name":        "Application",
		"description": "Applications",
		"archetypePolicy": map[string]any{
			"display": map[string]any{"label": "Application"},
		},
		"inducement": []any{map[string]any{
			"targetRef": map[string]any{"oid": endUserRoleOID, "type": "RoleType"},
		}},
	})
}

// expectSearch checks that the stub received exactly one search, on path
// with the Axiom filter text and a maxSize of 2.
func expectSearch(s *mpstub.Server, path, filter string) error {
	var searches []mpstub.Recorded
	for _, r := range requests(s, "POST") {
		if strings.HasSuffix(r.Path, "/search") {
			searches = append(searches, r)
		}
	}
	if len(searches) == 0 {
		return fmt.Errorf("no search received")
	}
	for _, r := range searches {
		query, _ := r.Body["query"].(map[string]any)
		f, _ := query["filter"].(map[string]any)
		paging, _ := query["paging"].(map[string]any)
		if r.Path != path || f["text"] != filter || fmt.Sprint(paging["maxSize"]) != "2" {
			return fmt.Errorf("want searches of %s for %q with maxSize 2, got %s %v", path, filter, r.Path, r.Body)
		}
	}
	return nil
}

// wantApplicationBody is the body the data source reports for the archetype
// putApplicationArchetype stores: canonical, without oid, "@id"s and the
// stub's server-managed noise.
var wantApplicationBody = knownvalue.ObjectExact(map[string]knownvalue.Check{
	"name":        knownvalue.StringExact("Application"),
	"description": knownvalue.StringExact("Applications"),
	"archetypePolicy": knownvalue.ObjectExact(map[string]knownvalue.Check{
		"display": knownvalue.ObjectExact(map[string]knownvalue.Check{"label": knownvalue.StringExact("Application")}),
	}),
	"inducement": knownvalue.ListExact([]knownvalue.Check{knownvalue.ObjectExact(map[string]knownvalue.Check{
		"targetRef": knownvalue.ObjectExact(map[string]knownvalue.Check{
			"oid":      knownvalue.StringExact(endUserRoleOID),
			"type":     knownvalue.StringExact("RoleType"),
			"relation": knownvalue.StringExact("org:default"),
		}),
	})}),
})

func TestDataObject_byName(t *testing.T) {
	s := newTestStub(t)
	putApplicationArchetype(s)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
data "midpoint_object" "test" {
  type = "archetypes"
  name = "Application"
}
`,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("id"), knownvalue.StringExact(dsArchetypeOID)),
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("oid"), knownvalue.StringExact(dsArchetypeOID)),
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("name"), knownvalue.StringExact("Application")),
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("body"), wantApplicationBody),
			},
			Check: checkStub(func() error {
				return expectSearch(s, "/archetypes/search", `name = "Application"`)
			}),
		}},
	})
}

func TestDataObject_byOID(t *testing.T) {
	s := newTestStub(t)
	putApplicationArchetype(s)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
data "midpoint_object" "test" {
  type = "archetypes"
  oid  = "` + dsArchetypeOID + `"
}
`,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("id"), knownvalue.StringExact(dsArchetypeOID)),
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("name"), knownvalue.StringExact("Application")),
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("body"), wantApplicationBody),
			},
		}},
	})
}

func TestDataObject_ambiguous(t *testing.T) {
	s := newTestStub(t)
	first := s.Put("roles", map[string]any{"name": "Twin"})
	second := s.Put("roles", map[string]any{"name": "Twin"})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
data "midpoint_object" "test" {
  type = "roles"
  name = "Twin"
}
`,
			ExpectError: regexp.MustCompile(`(?s)more\s+than\s+one\s+roles\s+named\s+"Twin".*` + first + `.*` + second + `.*oid`),
		}},
	})
}

func TestDataObject_notFound(t *testing.T) {
	s := newTestStub(t)
	s.Put("users", map[string]any{"name": "Application"})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: `
data "midpoint_object" "test" {
  type = "roles"
  name = "Application"
}
`,
				ExpectError: regexp.MustCompile(`no\s+roles\s+named\s+"Application"`),
			},
			{
				Config: `
data "midpoint_object" "test" {
  type = "roles"
  oid  = "` + objRoleOID + `"
}
`,
				ExpectError: regexp.MustCompile(`no\s+roles\s+with\s+oid\s+` + objRoleOID),
			},
		},
	})
}

func TestDataObject_exactlyOneOfOIDAndName(t *testing.T) {
	s := newTestStub(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config: `
data "midpoint_object" "test" {
  type = "roles"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Exactly\s+one\s+of\s+these\s+attributes\s+must\s+be\s+configured:\s+\[oid,name\]`),
			},
			{
				Config: `
data "midpoint_object" "test" {
  type = "roles"
  oid  = "` + objRoleOID + `"
  name = "x"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Exactly\s+one\s+of\s+these\s+attributes\s+must\s+be\s+configured:\s+\[oid,name\]`),
			},
		},
	})
}

func TestDataObject_bodyWithoutProtectedStrings(t *testing.T) {
	s := newTestStub(t)
	s.Put("users", map[string]any{
		"oid":  objRoleOID,
		"name": "alice",
		"credentials": map[string]any{"password": map[string]any{
			"value": map[string]any{"encryptedData": map[string]any{"cipherData": map[string]any{"cipherValue": "c2VjcmV0"}}},
		}},
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: `
data "midpoint_object" "test" {
  type = "users"
  name = "alice"
}
`,
			ConfigStateChecks: []statecheck.StateCheck{
				// credentials held nothing but the protected string, so it is gone.
				statecheck.ExpectKnownValue(dsObjectAddr, tfjsonpath.New("body"), knownvalue.ObjectExact(map[string]knownvalue.Check{
					"name": knownvalue.StringExact("alice"),
				})),
			},
		}},
	})
}
