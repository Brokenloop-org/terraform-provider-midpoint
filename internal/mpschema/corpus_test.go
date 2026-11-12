package mpschema

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
)

var allLines = []string{"4.9", "4.10", "4.11"}

func tableFor(t *testing.T, line string) *Table {
	t.Helper()
	tb, ok := For(line)
	if !ok {
		t.Fatalf("no table for %s", line)
	}
	return tb
}

func TestFor_allLines(t *testing.T) {
	sources := parseImages(t)
	for _, line := range allLines {
		tb := tableFor(t, line)
		if tb.Line != line {
			t.Errorf("%s: Line = %q", line, tb.Line)
		}
		if tb.Source != sources[line] {
			t.Errorf("%s: Source = %q, images.txt has %q", line, tb.Source, sources[line])
		}
		if len(tb.Types) == 0 || len(tb.Enums) == 0 || len(tb.Elements) == 0 {
			t.Errorf("%s: empty table: %d types, %d enums, %d elements", line, len(tb.Types), len(tb.Enums), len(tb.Elements))
		}
	}
	for version, want := range map[string]string{"4.9.3": "4.9", "4.10.5": "4.10", "4.11.0-SNAPSHOT": "4.11"} {
		tb, ok := For(version)
		if !ok || tb.Line != want {
			t.Errorf("For(%q) = %v, %v", version, tb, ok)
		}
	}
	for _, version := range []string{"4.12", "4.8.1", "", "unknown"} {
		if tb, ok := For(version); ok || tb != nil {
			t.Errorf("For(%q) = %v, %v, want none", version, tb, ok)
		}
	}
}

func parseImages(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("images.txt")
	if err != nil {
		t.Fatal(err)
	}
	digest := regexp.MustCompile(`^evolveum/midpoint:\S+-alpine@sha256:[0-9a-f]{64}$`)
	out := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		line, image, ok := strings.Cut(l, " ")
		if !ok || !digest.MatchString(image) {
			t.Fatalf("images.txt: bad line %q", l)
		}
		out[line] = image
	}
	return out
}

// restTypes are midPoint's REST collection types, from its ObjectTypes enum,
// and the type each one must resolve to through client.SingularOf.
var restTypes = map[string]string{
	"abstractRoles":                  "AbstractRoleType",
	"accessCertificationCampaigns":   "AccessCertificationCampaignType",
	"accessCertificationDefinitions": "AccessCertificationDefinitionType",
	"archetypes":                     "ArchetypeType",
	"assignmentHolders":              "AssignmentHolderType",
	"cases":                          "CaseType",
	"connectorHosts":                 "ConnectorHostType",
	"connectors":                     "ConnectorType",
	"dashboards":                     "DashboardType",
	"focus":                          "FocusType",
	"forms":                          "FormType",
	"functionLibraries":              "FunctionLibraryType",
	"genericObjects":                 "GenericObjectType",
	"lookupTables":                   "LookupTableType",
	"marks":                          "MarkType",
	"messageTemplates":               "MessageTemplateType",
	"nodes":                          "NodeType",
	"objectCollections":              "ObjectCollectionType",
	"objectTemplates":                "ObjectTemplateType",
	"objects":                        "ObjectType",
	"orgs":                           "OrgType",
	"policies":                       "PolicyType",
	"reportData":                     "ReportDataType",
	"reports":                        "ReportType",
	"resources":                      "ResourceType",
	"roleAnalysisCluster":            "RoleAnalysisClusterType",
	"roleAnalysisOutlier":            "RoleAnalysisOutlierType",
	"roleAnalysisSession":            "RoleAnalysisSessionType",
	"roles":                          "RoleType",
	"schemas":                        "SchemaType",
	"securityPolicies":               "SecurityPolicyType",
	"sequences":                      "SequenceType",
	"services":                       "ServiceType",
	"shadows":                        "ShadowType",
	"simulationResults":              "SimulationResultType",
	"systemConfigurations":           "SystemConfigurationType",
	"tasks":                          "TaskType",
	"users":                          "UserType",
	"valuePolicies":                  "ValuePolicyType",
}

func TestElements_everyRESTTypeResolves(t *testing.T) {
	for _, line := range allLines {
		tb := tableFor(t, line)
		for rest, want := range restTypes {
			if got := tb.Elements[client.SingularOf(rest)]; got != want {
				t.Errorf("%s: %s -> %s -> %q, want %s", line, rest, client.SingularOf(rest), got, want)
			}
			if _, ok := tb.Types[want]; !ok {
				t.Errorf("%s: type %s missing", line, want)
			}
		}
	}
}

// corpusValue is one value with the type it is checked as.
type corpusValue struct {
	typeName string
	value    any
}

// jsonRoots finds what a fixture holds: a {restType, body} pair, or a single
// wrapper key naming a global element. A search result is an object list
// wrapper, whose objects each state their own type.
func jsonRoots(tb *Table, doc any) []corpusValue {
	m, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	if rest, ok := m["restType"].(string); ok {
		ty, known := tb.Elements[client.SingularOf(rest)]
		if !known || m["body"] == nil {
			return nil
		}
		return []corpusValue{{ty, m["body"]}}
	}
	var key string
	for k := range m {
		if strings.HasPrefix(k, "@") {
			continue
		}
		if key != "" {
			return nil
		}
		key = k
	}
	if key == "" {
		return nil
	}
	val, isMap := m[key].(map[string]any)
	if !isMap {
		return nil
	}
	if ty, _ := val["@type"].(string); strings.HasSuffix(ty, "#ObjectListType") {
		list, _ := val["object"].([]any)
		roots := make([]corpusValue, 0, len(list))
		for _, o := range list {
			roots = append(roots, corpusValue{"ObjectType", o})
		}
		return roots
	}
	ty, known := tb.Elements[stripPrefix(key)]
	if !known {
		return nil
	}
	return []corpusValue{{ty, val}}
}

func jsonFixtures(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{"../normalize/testdata", "../delta/testdata"} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Ext(p) == ".json" {
				files = append(files, p)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		t.Fatal("no JSON fixtures found")
	}
	return files
}

// inventedFixtureData lists findings that are right: these hand-written fixtures
// exercise the normalizer with data midPoint does not have. Fixtures taken from a
// real server (real/, real_*) have no entry. Only the exact path is excused, the
// rest of each file is still checked, and an entry no longer found fails the test.
var inventedFixtureData = map[string]string{
	"folds_polystring_name.in.json: archetypePolicy":                             "an archetype's item, not a role's",
	"folds_polystring_name.in.json: identifier":                                  "a string item, given as a polystring to test folding",
	"qname_values_normalized.in.json: #KeyType":                                  "tests a # key",
	"qname_values_normalized.in.json: adminGuiConfiguration.objectDetails.panel": "the item is objectDetailsPage",
	"reference_type_prefix.in.json: adminGuiConfiguration.objectDetails.panel":   "the item is objectDetailsPage",
	"reference_type_prefix.in.json: approverRef":                                 "not an item of a role",
	"single_value_container_becomes_list.in.json: alreadyList":                   "made up to test list handling",
	"single_value_container_becomes_list.in.json: policyRule":                    "an assignment's item, not a role's",
	"strips_key_prefixes.in.json: inducement[0].c:condition.q:filter":            "a mapping has no filter",
	"strips_metadata_and_version.in.json: activation.validityStatus":             "the values are before, in and after",
}

func TestCorpus_noFalseAlarms(t *testing.T) {
	files := jsonFixtures(t)
	for _, line := range allLines {
		tb := tableFor(t, line)
		checked, skipped := 0, 0
		unused := maps.Clone(inventedFixtureData)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var doc any
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			roots := jsonRoots(tb, doc)
			if len(roots) == 0 {
				if strings.Contains(filepath.ToSlash(f), "/real/") {
					t.Errorf("%s: a file taken from a real server resolves to no root", f)
				}
				skipped++
				continue
			}
			checked++
			for _, r := range roots {
				for _, finding := range tb.Check(r.typeName, r.value) {
					key := filepath.Base(f) + ": " + finding.Path
					if _, invented := inventedFixtureData[key]; invented {
						delete(unused, key)
						continue
					}
					t.Errorf("%s: %s: %s (%s): %s", line, f, finding.Path, finding.Type, finding.Problem)
				}
			}
		}
		for key := range unused {
			t.Errorf("%s: inventedFixtureData entry %q no longer applies", line, key)
		}
		t.Logf("%s: %d JSON files checked, %d with no identifiable root skipped", line, checked, skipped)
		if checked == 0 {
			t.Errorf("%s: nothing checked", line)
		}
	}
}

// yamlRoots says what each YAML file under examples/ and testacc/fixtures holds.
// A new file fails TestCorpus_yaml until it is listed here.
var yamlRoots = map[string]string{
	"../../examples/resources/midpoint_object/report.yaml":                   "ReportType",
	"../../examples/resources/midpoint_resource/schema-handling.yaml":        "SchemaHandlingType",
	"../../examples/resources/midpoint_role/only-employees.yaml":             "MappingType",
	"../../testacc/fixtures/csv-resource/schema-handling.yaml":               "SchemaHandlingType",
	"../../testacc/fixtures/csv-resource/schema-handling-extra-inbound.yaml": "SchemaHandlingType",
}

// yamlUnchecked lists YAML files whose content is defined per connector.
var yamlUnchecked = []string{
	"../../examples/resources/midpoint_resource/connector-configuration.yaml",
	"../../testacc/fixtures/csv-resource/connector-configuration.yaml",
	"../../testacc/fixtures/ldap-resource/connector-configuration.yaml",
}

func TestCorpus_yaml(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../examples", "../../testacc/fixtures"} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (filepath.Ext(p) == ".yaml" || filepath.Ext(p) == ".yml") {
				files = append(files, filepath.ToSlash(p))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		if slices.Contains(yamlUnchecked, f) {
			continue
		}
		root, known := yamlRoots[f]
		if !known {
			t.Errorf("%s: add it to yamlRoots or yamlUnchecked", f)
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, line := range allLines {
			for _, finding := range tableFor(t, line).Check(root, doc) {
				t.Errorf("%s: %s: %s (%s): %s", line, f, finding.Path, finding.Type, finding.Problem)
			}
		}
	}
	for f := range yamlRoots {
		if !slices.Contains(files, f) {
			t.Errorf("yamlRoots lists %s, which does not exist", f)
		}
	}
}

func TestCorpus_knownTypos(t *testing.T) {
	cases := []struct {
		typeName, key string
		value         any
	}{
		{"ResourceType", "schemaHandlng", map[string]any{}},
		{"SchemaHandlingType", "objectTyep", []any{}},
		{"ResourceObjectTypeDefinitionType", "attibute", []any{}},
		{"RoleType", "descripton", "x"},
		{"MappingType", "strenght", "strong"},
		{"MappingType", "strength", "strng"},
	}
	for _, line := range allLines {
		tb := tableFor(t, line)
		for _, c := range cases {
			got := tb.Check(c.typeName, map[string]any{c.key: c.value})
			if len(got) != 1 || got[0].Key != c.key {
				t.Errorf("%s: %s.%s = %+v, want one finding", line, c.typeName, c.key, got)
			}
		}
	}
}

// An item holding several values through its substitution head must not be
// taken for single-valued: expression evaluators are the real case.
func TestCorpus_substitutionHeadIsMulti(t *testing.T) {
	for _, line := range allLines {
		tb := tableFor(t, line)
		v := map[string]any{"script": []any{map[string]any{"code": "a"}, map[string]any{"code": "b"}}}
		if got := tb.Check("ExpressionType", v); len(got) != 0 {
			t.Errorf("%s: %+v", line, got)
		}
		if got := tb.Check("ExpressionType", map[string]any{"script": map[string]any{"cod": "a"}}); len(got) != 1 {
			t.Errorf("%s: a typo under a member must follow the member's own type: %+v", line, got)
		}
	}
}
