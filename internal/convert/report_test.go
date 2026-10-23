package convert

import (
	"strings"
	"testing"
)

func TestReport_sectionsAndDeterminism(t *testing.T) {
	srcs := []Source{{Path: "b/two.json"}, {Path: "a/one.json"}}
	rs := []Resource{
		{Kind: "midpoint_role", Label: "two", Notes: []string{"extra: lifecycleState", "manual follow-up: set the connector by hand", "capabilities: dropped x"}},
		{Kind: "midpoint_resource", Label: "one"},
	}
	loadNotes := []Note{
		{"z/tmpl.json", "skipped: template file, not an object"},
		{"a/one.json", "dropped server-managed items: metadata"},
	}
	rewriteNotes := []Note{{"midpoint_role.two", "reference 1 is not converted and not a built-in object; left as a literal oid"}}
	data := []DataLookup{{Label: "user_2", RestType: "users", OID: "00000000-0000-0000-0000-000000000002"}}

	got := string(newReport(srcs, rs, data, loadNotes, rewriteNotes).Markdown())
	for _, want := range []string{
		"| Files read | 3 |", "| Objects converted | 2 |", "| Files skipped | 1 |", "| Data lookups | 1 |", "| Manual follow-up items | 1 |",
		"## Manual follow-up\n\nThese need a human decision before `terraform apply`.\n\n- `midpoint_role.two` (`b/two.json`): set the connector by hand\n",
		"- `z/tmpl.json`: template file, not an object\n",
		"### `a/one.json`\n\nResource: `midpoint_resource.one`\n\n- dropped as server-managed: metadata\n",
		"- placed in extra: extra: lifecycleState\n",
		"- references: reference 1 is not converted",
		"- other: capabilities: dropped x\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	if tbl := got[strings.Index(got, "## Converted objects"):]; strings.Index(tbl, "a/one.json") > strings.Index(tbl, "b/two.json") {
		t.Error("entries are not sorted by path")
	}
	if again := string(newReport(srcs, rs, data, loadNotes, rewriteNotes).Markdown()); again != got {
		t.Error("report is not deterministic")
	}
}

func TestReport_unmatchedRewriteNotesAreKept(t *testing.T) {
	srcs := []Source{{Path: "a.json"}}
	rs := []Resource{{Kind: "midpoint_role", Label: "a"}}
	notes := []Note{
		{"midpoint_role.a", "matched note"},
		{"midpoint_role.ghost", "unmatched note"},
	}
	got := string(newReport(srcs, rs, nil, nil, notes).Markdown())
	for _, want := range []string{"matched note", "## Other notes\n\n- `midpoint_role.ghost`: unmatched note\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}
