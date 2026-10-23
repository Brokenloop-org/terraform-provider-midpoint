package convert

// Convert runs the whole conversion on the object files below roots: load,
// project, dedupe labels, rewrite references and render. It returns the
// files to write, relative to the output directory (report.md is not among
// them, see Report.Markdown), and the report. It never contacts a server.
// A *ConflictError from Load is returned as is.
func Convert(roots []string, o Options) (map[string][]byte, Report, error) {
	srcs, loadNotes, err := Load(roots)
	if err != nil {
		return nil, Report{}, err
	}
	rs := make([]Resource, 0, len(srcs))
	for _, s := range srcs {
		r, err := Project(s)
		if err != nil {
			return nil, Report{}, err
		}
		rs = append(rs, r)
	}
	DedupeLabels(rs)
	rs, data, rewriteNotes := Rewrite(rs)
	files, err := Render(rs, data, o)
	if err != nil {
		return nil, Report{}, err
	}
	return files, newReport(srcs, rs, data, loadNotes, rewriteNotes), nil
}
