package convert

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// hclRenderer writes HCL native syntax.
type hclRenderer struct{}

func (hclRenderer) ext() string { return ".tf" }

func (hclRenderer) resources(docs []resourceDoc) ([]byte, error) {
	var b strings.Builder
	for i, d := range docs {
		if i > 0 {
			b.WriteString("\n")
		}
		if err := writeResource(&b, d); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", d.kind, d.label, err)
		}
	}
	return finishHCL(b.String())
}

func (hclRenderer) data(list []DataLookup) ([]byte, error) {
	var b strings.Builder
	for i, d := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "data \"midpoint_object\" %s {\n  type = %s\n  oid = %s\n}\n", quote(d.Label), quote(d.RestType), quote(d.OID))
	}
	return finishHCL(b.String())
}

func (hclRenderer) imports(list []importDoc) ([]byte, error) {
	var b strings.Builder
	for i, im := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		if !validTraversal(im.to) {
			return nil, fmt.Errorf("invalid import address %q", im.to)
		}
		fmt.Fprintf(&b, "import {\n  to = %s\n  id = %s\n}\n", im.to, quote(im.id))
	}
	return finishHCL(b.String())
}

// finishHCL formats src and checks that it parses, so that a renderer bug
// fails the conversion instead of producing a broken file.
func finishHCL(src string) ([]byte, error) {
	out := hclwrite.Format([]byte(src))
	if _, diags := hclsyntax.ParseConfig(out, "generated.tf", hcl.InitialPos); diags.HasErrors() {
		return nil, fmt.Errorf("generated HCL does not parse: %s", diags.Error())
	}
	return out, nil
}

// writeResource writes one resource block: the single-line attributes, then
// each multi-line attribute and each block separated by a blank line.
func writeResource(b *strings.Builder, d resourceDoc) error {
	fmt.Fprintf(b, "resource %s %s {\n", quote(d.kind), quote(d.label))
	for _, c := range d.comments {
		fmt.Fprintf(b, "  # %s\n", c)
	}
	if err := writeBody(b, d.attrs, d.blocks, 1, false); err != nil {
		return err
	}
	b.WriteString("}\n")
	return nil
}

// writeBody writes the attributes and blocks of a body at depth (the
// nesting level of its lines). In a block body (inBlock), map and list
// values are written as jsonencode(...).
func writeBody(b *strings.Builder, attrs []attr, blocks []Block, depth int, inBlock bool) error {
	var short, long []string
	for _, a := range attrs {
		w := &hclWriter{}
		w.b.WriteString(a.name + " = ")
		var err error
		if inBlock && isFreeForm(a.value) {
			err = w.call("jsonencode", a.value, depth)
		} else {
			err = w.value(a.value, depth)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", a.name, err)
		}
		if text := w.b.String(); strings.Contains(text, "\n") {
			long = append(long, text)
		} else {
			short = append(short, text)
		}
	}
	sep := false
	for _, s := range short {
		b.WriteString(s + "\n")
		sep = true
	}
	for _, l := range long {
		if sep {
			b.WriteString("\n")
		}
		b.WriteString(l + "\n")
		sep = true
	}
	for _, blk := range blocks {
		if sep {
			b.WriteString("\n")
		}
		sep = true
		if !hclsyntax.ValidIdentifier(blk.Type) {
			return fmt.Errorf("invalid block type %q", blk.Type)
		}
		b.WriteString(blk.Type + " {\n")
		var as []attr
		for _, k := range orderedNames(blk.Attrs, blockAttrOrder) {
			as = append(as, attr{k, blk.Attrs[k]})
		}
		if err := writeBody(b, as, blk.Blocks, depth+1, true); err != nil {
			return fmt.Errorf("%s: %w", blk.Type, err)
		}
		b.WriteString("}\n")
	}
	return nil
}

// hclWriter writes one HCL expression. Indentation is left to
// hclwrite.Format, except inside heredocs, which it never touches: there the
// writer indents by the depth it is given. An inline writer writes the
// expression on one line, without heredocs (for Terraform JSON templates).
type hclWriter struct {
	b      strings.Builder
	inline bool
}

// call writes fn(v), e.g. jsonencode({ ... }).
func (w *hclWriter) call(fn string, v any, depth int) error {
	w.b.WriteString(fn + "(")
	heredoc, err := w.valueAt(v, depth)
	if err != nil {
		return err
	}
	if heredoc {
		w.b.WriteString("\n")
	}
	w.b.WriteString(")")
	return nil
}

// value writes v; depth is the nesting level of the line v starts on. A
// value may end with a heredoc closing marker: the caller ends the line.
func (w *hclWriter) value(v any, depth int) error {
	_, err := w.valueAt(v, depth)
	return err
}

// valueAt writes v and reports whether it ended with a heredoc closing
// marker (after which only a newline may follow).
func (w *hclWriter) valueAt(v any, depth int) (heredoc bool, err error) {
	switch t := v.(type) {
	case nil:
		w.b.WriteString("null")
	case string:
		if !utf8.ValidString(t) {
			return false, fmt.Errorf("string %q is not valid UTF-8", t)
		}
		if !w.inline && heredocable(t) {
			w.heredoc(t, depth)
			return strings.HasSuffix(t, "\n"), nil
		}
		w.b.WriteString(quote(t))
	case bool:
		w.b.WriteString(strconv.FormatBool(t))
	case int:
		w.b.WriteString(strconv.Itoa(t))
	case int64:
		w.b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		if math.IsInf(t, 0) || math.IsNaN(t) {
			return false, fmt.Errorf("number %v has no HCL form", t)
		}
		w.b.WriteString(strconv.FormatFloat(t, 'f', -1, 64))
	case json.Number:
		if _, err := strconv.ParseFloat(string(t), 64); err != nil {
			return false, fmt.Errorf("invalid number %q", t)
		}
		w.b.WriteString(string(t))
	case Ref:
		if !validTraversal(t.Address) {
			return false, fmt.Errorf("invalid reference %q", t.Address)
		}
		w.b.WriteString(t.Address)
	case expr:
		w.b.WriteString(t.text)
	case map[string]any:
		return w.object(t, depth)
	case []any:
		return w.tuple(t, depth)
	default:
		return false, fmt.Errorf("unsupported value of type %s", reflect.TypeOf(v))
	}
	return false, nil
}

func (w *hclWriter) object(m map[string]any, depth int) (bool, error) {
	if len(m) == 0 {
		w.b.WriteString("{}")
		return false, nil
	}
	// An object of a few short scalars stays on one line.
	if !w.inline && isShortScalarObject(m) {
		iw := &hclWriter{inline: true}
		if _, err := iw.object(m, depth); err != nil {
			return false, err
		}
		w.b.WriteString(iw.b.String())
		return false, nil
	}
	if w.inline {
		w.b.WriteString("{ ")
	} else {
		w.b.WriteString("{\n")
	}
	for i, k := range sortedKeys(m) {
		if w.inline && i > 0 {
			w.b.WriteString(", ")
		}
		w.b.WriteString(objectKey(k) + " = ")
		if _, err := w.valueAt(m[k], depth+1); err != nil {
			return false, fmt.Errorf("%s: %w", k, err)
		}
		if !w.inline {
			w.b.WriteString("\n")
		}
	}
	if w.inline {
		w.b.WriteString(" }")
	} else {
		w.b.WriteString("}")
	}
	return false, nil
}

func (w *hclWriter) tuple(l []any, depth int) (bool, error) {
	if len(l) == 0 {
		w.b.WriteString("[]")
		return false, nil
	}
	// A list of short scalars stays on one line.
	if !w.inline && isShortScalarList(l) {
		iw := &hclWriter{inline: true}
		if _, err := iw.tuple(l, depth); err != nil {
			return false, err
		}
		w.b.WriteString(iw.b.String())
		return false, nil
	}
	if w.inline {
		w.b.WriteString("[")
	} else {
		w.b.WriteString("[\n")
	}
	for i, e := range l {
		if w.inline && i > 0 {
			w.b.WriteString(", ")
		}
		heredoc, err := w.valueAt(e, depth+1)
		if err != nil {
			return false, fmt.Errorf("[%d]: %w", i, err)
		}
		if !w.inline {
			if heredoc {
				w.b.WriteString("\n")
			}
			w.b.WriteString(",\n")
		}
	}
	w.b.WriteString("]")
	return false, nil
}

// isShortScalarList reports whether l holds only scalars and Refs and
// written inline fits in 80 columns.
func isShortScalarList(l []any) bool {
	n := 2
	for _, e := range l {
		w, ok := scalarWidth(e)
		if !ok {
			return false
		}
		n += w + 2
	}
	return n <= 80
}

// isShortScalarObject reports whether m holds at most three scalars and
// Refs and written inline fits in 60 columns.
func isShortScalarObject(m map[string]any) bool {
	if len(m) > 3 {
		return false
	}
	n := 4
	for k, e := range m {
		w, ok := scalarWidth(e)
		if !ok {
			return false
		}
		n += len(objectKey(k)) + w + 5
	}
	return n <= 60
}

// scalarWidth is the approximate width of the scalar or Ref v written
// inline; ok is false for anything else, a multi-line string included.
func scalarWidth(v any) (int, bool) {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "\n") {
			return 0, false
		}
		return len(quote(t)), true
	case Ref:
		return len(t.Address), true
	case bool, int, int64, float64, json.Number, nil:
		return 8, true
	}
	return 0, false
}

// heredocable reports whether s is written as a heredoc: it spans lines and
// holds no carriage return or other control character a heredoc cannot
// carry verbatim.
func heredocable(s string) bool {
	if !strings.Contains(s, "\n") || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

// heredoc writes s as a heredoc whose value is exactly s. Lines are indented
// one level below depth with the flush form <<-, which strips the smallest
// indentation of the non-blank lines; that is exact only when some line of
// s starts without white space, otherwise the verbatim form << is used. A
// string without a final newline is wrapped in chomp(), since a heredoc
// always ends with one.
func (w *hclWriter) heredoc(s string, depth int) {
	delim := "EOT"
	for n := 1; hasLine(s, delim); n++ {
		delim = fmt.Sprintf("EOT%d", n)
	}
	chomp := !strings.HasSuffix(s, "\n")
	if chomp {
		w.b.WriteString("chomp(")
		s += "\n"
	}
	flush := flushable(s)
	indent := strings.Repeat("  ", depth+1)
	if flush {
		w.b.WriteString("<<-" + delim + "\n")
	} else {
		w.b.WriteString("<<" + delim + "\n")
	}
	for _, line := range strings.SplitAfter(strings.TrimSuffix(s, "\n"), "\n") {
		line = strings.TrimSuffix(line, "\n")
		if flush && strings.TrimLeftFunc(line, unicode.IsSpace) != "" {
			w.b.WriteString(indent)
		}
		w.b.WriteString(escapeTemplate(line) + "\n")
	}
	w.b.WriteString(strings.Repeat("  ", depth) + delim)
	if chomp {
		w.b.WriteString("\n" + strings.Repeat("  ", depth) + ")")
	}
}

// hasLine reports whether a line of s is delim once trimmed (it would close
// the heredoc).
func hasLine(s, delim string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == delim {
			return true
		}
	}
	return false
}

// flushable reports whether s, every line ending in a newline, keeps its
// exact value in a flush heredoc after indenting its non-blank lines: some
// non-blank line starts without white space (so the indentation stripped is
// exactly the one added), and no line starts with a combining mark (which
// would join the added indentation).
func flushable(s string) bool {
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if strings.TrimLeftFunc(line, unicode.IsSpace) == "" {
			continue // blank: not counted nor stripped by HCL
		}
		r, _ := utf8.DecodeRuneInString(line)
		if unicode.Is(unicode.M, r) {
			return false
		}
		if !unicode.IsSpace(r) {
			found = true
		}
	}
	return found
}

// quote is s as an HCL quoted string literal, template sequences escaped.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '$', '%':
			b.WriteRune(r)
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteRune(r)
			}
		default:
			switch {
			case !unicode.IsPrint(r) && r < 0x10000:
				fmt.Fprintf(&b, `\u%04x`, r)
			case !unicode.IsPrint(r):
				fmt.Fprintf(&b, `\U%08x`, r)
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// objectKey is k as an object constructor key: bare when it is an
// identifier that cannot be read as something else, quoted otherwise.
func objectKey(k string) string {
	switch k {
	case "for", "in", "if", "true", "false", "null":
		return quote(k)
	}
	if hclsyntax.ValidIdentifier(k) {
		return k
	}
	return quote(k)
}

// validTraversal reports whether s is a dotted traversal of identifiers.
func validTraversal(s string) bool {
	parts := strings.Split(s, ".")
	for _, p := range parts {
		if !hclsyntax.ValidIdentifier(p) {
			return false
		}
	}
	return len(parts) > 1
}
