package convert

import (
	"fmt"
	"sort"
	"strings"
)

// Report describes what a conversion did, per source file. Markdown renders
// it deterministically.
type Report struct {
	entries []entry // sorted by source path
	skipped []Note  // skipped files and duplicates, sorted by path
	other   []Note  // notes that match no converted object, sorted
	data    []DataLookup
	read    int
}

// entry is one converted object.
type entry struct {
	path     string
	address  string   // midpoint_role.label
	dropped  []string // server-managed items Load dropped
	extra    []string // items placed in extra
	refs     []string // references left literal, filter-only or mismatched
	other    []string // other projection notes
	followUp []string // manual follow-up items, without their prefix
}

const followUpPrefix = "manual follow-up:"

func newReport(srcs []Source, rs []Resource, data []DataLookup, loadNotes, rewriteNotes []Note) Report {
	rep := Report{data: data}
	byAddress := map[string]int{}
	byPath := map[string]int{}
	for i, s := range srcs {
		addr := rs[i].Kind + "." + rs[i].Label
		rep.entries = append(rep.entries, entry{path: s.Path, address: addr})
		byAddress[addr] = i
		byPath[s.Path] = i
	}
	for _, n := range loadNotes {
		if i, ok := byPath[n.Path]; ok {
			rep.entries[i].dropped = append(rep.entries[i].dropped, strings.TrimPrefix(n.Message, "dropped server-managed items: "))
		} else {
			rep.skipped = append(rep.skipped, n)
		}
	}
	for i, r := range rs {
		e := &rep.entries[i]
		for _, n := range r.Notes {
			switch {
			case strings.HasPrefix(n, followUpPrefix):
				e.followUp = append(e.followUp, strings.TrimSpace(strings.TrimPrefix(n, followUpPrefix)))
			case strings.HasPrefix(n, "extra: ") || strings.Contains(n, "kept in extra"):
				e.extra = append(e.extra, n)
			default:
				e.other = append(e.other, n)
			}
		}
	}
	for _, n := range rewriteNotes {
		if i, ok := byAddress[n.Path]; ok {
			rep.entries[i].refs = append(rep.entries[i].refs, n.Message)
		} else {
			rep.other = append(rep.other, n)
		}
	}
	rep.read = len(srcs) + len(rep.skipped)
	sort.SliceStable(rep.entries, func(i, j int) bool { return rep.entries[i].path < rep.entries[j].path })
	sort.SliceStable(rep.other, func(i, j int) bool {
		if rep.other[i].Path != rep.other[j].Path {
			return rep.other[i].Path < rep.other[j].Path
		}
		return rep.other[i].Message < rep.other[j].Message
	})
	sort.SliceStable(rep.skipped, func(i, j int) bool { return rep.skipped[i].Path < rep.skipped[j].Path })
	for i := range rep.entries {
		e := &rep.entries[i]
		for _, l := range [][]string{e.dropped, e.extra, e.refs, e.other, e.followUp} {
			sort.Strings(l)
		}
	}
	return rep
}

// Markdown renders the report. The sections are Summary, Manual follow-up,
// Skipped files, Data lookups, Converted objects (one table row per source
// file) and Notes by file (one ### per file that has notes) and Other notes (notes that match no converted object).
func (r Report) Markdown() []byte {
	var b strings.Builder
	b.WriteString("# midpoint-convert report\n")

	b.WriteString("\n## Summary\n\n| | Count |\n|---|---|\n")
	fmt.Fprintf(&b, "| Files read | %d |\n| Objects converted | %d |\n| Files skipped | %d |\n| Data lookups | %d |\n",
		r.read, len(r.entries), len(r.skipped), len(r.data))
	n := 0
	for _, e := range r.entries {
		n += len(e.followUp)
	}
	fmt.Fprintf(&b, "| Manual follow-up items | %d |\n", n)

	b.WriteString("\n## Manual follow-up\n\n")
	if n == 0 {
		b.WriteString("None.\n")
	} else {
		b.WriteString("These need a human decision before `terraform apply`.\n\n")
		for _, e := range r.entries {
			for _, f := range e.followUp {
				fmt.Fprintf(&b, "- `%s` (`%s`): %s\n", e.address, e.path, f)
			}
		}
	}

	b.WriteString("\n## Skipped files\n\n")
	if len(r.skipped) == 0 {
		b.WriteString("None.\n")
	}
	for _, s := range r.skipped {
		fmt.Fprintf(&b, "- `%s`: %s\n", s.Path, strings.TrimPrefix(s.Message, "skipped: "))
	}

	b.WriteString("\n## Data lookups\n\n")
	if len(r.data) == 0 {
		b.WriteString("None.\n")
	} else {
		b.WriteString("Built-in objects outside the converted set, looked up by oid.\n\n| Address | Type | OID |\n|---|---|---|\n")
		for _, d := range r.data {
			fmt.Fprintf(&b, "| `data.midpoint_object.%s` | %s | %s |\n", d.Label, d.RestType, d.OID)
		}
	}

	b.WriteString("\n## Converted objects\n\n| Source file | Resource |\n|---|---|\n")
	for _, e := range r.entries {
		fmt.Fprintf(&b, "| `%s` | `%s` |\n", e.path, e.address)
	}

	b.WriteString("\n## Notes by file\n")
	any := false
	for _, e := range r.entries {
		if len(e.dropped)+len(e.extra)+len(e.refs)+len(e.other) == 0 {
			continue
		}
		any = true
		fmt.Fprintf(&b, "\n### `%s`\n\nResource: `%s`\n\n", e.path, e.address)
		for _, g := range []struct {
			title string
			items []string
		}{
			{"dropped as server-managed", e.dropped},
			{"placed in extra", e.extra},
			{"references", e.refs},
			{"other", e.other},
		} {
			for _, it := range g.items {
				fmt.Fprintf(&b, "- %s: %s\n", g.title, it)
			}
		}
	}
	if !any {
		b.WriteString("\nNone.\n")
	}

	b.WriteString("\n## Other notes\n\n")
	if len(r.other) == 0 {
		b.WriteString("None.\n")
	}
	for _, n := range r.other {
		fmt.Fprintf(&b, "- `%s`: %s\n", n.Path, n.Message)
	}
	return []byte(b.String())
}
