package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpschema"
)

const (
	xsdNS        = "http://www.w3.org/2001/XMLSchema"
	annotationNS = "http://prism.evolveum.com/xml/ns/public/annotation-3"
	commonNS     = "http://midpoint.evolveum.com/xml/ns/public/common/common-3"
)

// node is one element of a parsed XSD file. The tree keeps namespace
// declarations, because QNames in attribute values resolve against them.
type node struct {
	name     xml.Name
	attrs    map[string]string
	ns       map[string]string
	parent   *node
	children []*node
	text     string
}

func (n *node) is(local string) bool { return n.name.Space == xsdNS && n.name.Local == local }

type qname struct{ ns, local string }

// resolve turns "prefix:local" into a qname using the declarations in scope.
func (n *node) resolve(s string) qname {
	prefix, local, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		prefix, local = "", prefix
	}
	for cur := n; cur != nil; cur = cur.parent {
		if uri, ok := cur.ns[prefix]; ok {
			return qname{uri, local}
		}
	}
	return qname{"", local}
}

// appinfo returns the text of a Prism annotation on n, if it has one.
func (n *node) appinfo(local string) (string, bool) {
	for _, ann := range n.children {
		if !ann.is("annotation") {
			continue
		}
		for _, info := range ann.children {
			if !info.is("appinfo") {
				continue
			}
			for _, c := range info.children {
				if c.name.Space == annotationNS && c.name.Local == local {
					return strings.TrimSpace(c.text), true
				}
			}
		}
	}
	return "", false
}

// repeats reports whether n's own occurrence limits allow more than one value.
func (n *node) repeats() bool {
	if m, ok := n.attrs["maxOccurs"]; ok && m != "1" {
		return true
	}
	m, ok := n.appinfo("maxOccurs")
	return ok && m != "1"
}

func parseFile(path string) (*node, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := xml.NewDecoder(f)
	var root, cur *node
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if root == nil || cur != nil {
				return nil, errors.New("no complete document")
			}
			return root, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name, attrs: map[string]string{}, ns: map[string]string{}, parent: cur}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "xmlns":
					n.ns[a.Name.Local] = a.Value
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					n.ns[""] = a.Value
				case a.Name.Space == "":
					n.attrs[a.Name.Local] = a.Value
				}
			}
			if cur == nil {
				root = n
			} else {
				cur.children = append(cur.children, n)
			}
			cur = n
		case xml.EndElement:
			cur = cur.parent
		case xml.CharData:
			if cur != nil {
				cur.text += string(t)
			}
		}
	}
}

// skippedPath reports the XSDs the table leaves out: test schemas, and the
// copies of the Prism schemas that schema-<v>.jar carries under prism-impl/.
func skippedPath(rel string) bool {
	rel = "/" + filepath.ToSlash(rel)
	return strings.Contains(rel, "/xml/ns/test/") || strings.Contains(rel, "/prism-impl/")
}

// schemaFiles reads every XSD below dir, in a stable order.
func schemaFiles(dir string) ([]*node, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".xsd" {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || skippedPath(rel) {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no XSD files under %s", dir)
	}
	slices.Sort(paths)
	roots := make([]*node, 0, len(paths))
	for _, p := range paths {
		root, err := parseFile(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		roots = append(roots, root)
	}
	return roots, nil
}

// flatType is a complex type with its keys flattened.
type flatType struct {
	items map[string]mpschema.Item
	open  bool
}

// namedDef is a complex type definition with the local name the table gives it.
type namedDef struct {
	name string
	node *node
}

type builder struct {
	elements   map[qname]*node
	attributes map[qname]*node
	complex    map[qname]*node
	simple     map[qname]*node
	groups     map[qname]*node
	attrGroups map[qname]*node
	members    map[qname][]qname

	globalElements []*node
	complexDefs    []namedDef       // grows as anonymous types are met
	anonNames      map[*node]string // anonymous complex types already queued
	flat           map[*node]*flatType
	visiting       map[*node]bool
	enums          map[string][][]string
	simpleNames    map[string]bool
}

func newBuilder(roots []*node) *builder {
	b := &builder{
		elements:    map[qname]*node{},
		attributes:  map[qname]*node{},
		complex:     map[qname]*node{},
		simple:      map[qname]*node{},
		groups:      map[qname]*node{},
		attrGroups:  map[qname]*node{},
		members:     map[qname][]qname{},
		anonNames:   map[*node]string{},
		flat:        map[*node]*flatType{},
		visiting:    map[*node]bool{},
		enums:       map[string][][]string{},
		simpleNames: map[string]bool{},
	}
	for _, root := range roots {
		target := root.attrs["targetNamespace"]
		for _, n := range root.children {
			q := qname{target, n.attrs["name"]}
			switch {
			case n.is("element"):
				b.elements[q] = n
				b.globalElements = append(b.globalElements, n)
				for _, head := range strings.Fields(n.attrs["substitutionGroup"]) {
					h := n.resolve(head)
					b.members[h] = append(b.members[h], q)
				}
			case n.is("attribute"):
				b.attributes[q] = n
			case n.is("complexType"):
				b.complex[q] = n
				b.complexDefs = append(b.complexDefs, namedDef{q.local, n})
			case n.is("simpleType"):
				b.simple[q] = n
			case n.is("group"):
				b.groups[q] = n
			case n.is("attributeGroup"):
				b.attrGroups[q] = n
			}
		}
	}
	return b
}

// parseDir builds the table of the XSD files below dir.
func parseDir(dir string) (*mpschema.Table, error) {
	roots, err := schemaFiles(dir)
	if err != nil {
		return nil, err
	}
	b := newBuilder(roots)
	return b.table(roots), nil
}

func (b *builder) table(roots []*node) *mpschema.Table {
	// Named simple types are classified first, so that a type's name and values
	// are known to the collision checks below even if no item uses them.
	for _, root := range roots {
		for _, n := range root.children {
			if n.is("simpleType") {
				b.simpleNames[n.attrs["name"]] = true
				b.classifySimple(n, n.attrs["name"])
			}
		}
	}
	elements, conflicting := b.elementTypes()
	// complexDefs grows while it is walked: flattening meets anonymous types.
	byName := map[string][]*flatType{}
	for i := 0; i < len(b.complexDefs); i++ {
		def := b.complexDefs[i]
		byName[def.name] = append(byName[def.name], b.flatten(def.node, def.name))
	}
	// Prism takes a primitive "id" as the container id when a definition has no
	// such item (PrismUnmarshaller.isContainerId), so it is valid even where the
	// XSD leaves it out. midPoint's Java classes, not the XSD alone, decide what
	// is a container, so every complex type takes it. This runs after flattening
	// so that a child declaring its own id does not meet an implied one.
	for _, defs := range byName {
		for _, f := range defs {
			if _, declared := f.items["id"]; !declared {
				f.items["id"] = mpschema.Item{Kind: mpschema.KindInteger}
			}
		}
	}

	t := &mpschema.Table{
		Types:    map[string]*mpschema.Type{},
		Enums:    map[string][]string{},
		Elements: map[string]string{},
	}
	for name, defs := range b.enums {
		if allSameEnum(defs) {
			t.Enums[name] = defs[0]
		}
	}
	for name, defs := range byName {
		if !allSameType(defs) || b.simpleNames[name] {
			// JSON and @type carry no namespace, so the name alone cannot say
			// which definition a value follows.
			t.Types[name] = &mpschema.Type{Open: true}
			continue
		}
		t.Types[name] = &mpschema.Type{Items: defs[0].items, Open: defs[0].open}
	}
	for _, ty := range t.Types {
		for key, it := range ty.Items {
			if it.Kind == mpschema.KindEnum {
				if _, ok := t.Enums[it.Type]; !ok {
					ty.Items[key] = mpschema.Item{Kind: mpschema.KindSimple, Multi: it.Multi}
				}
			}
		}
	}
	for name, ty := range elements {
		if !conflicting[name] {
			t.Elements[name] = ty
		}
	}
	for name, ty := range b.objectElementTypes() {
		t.Elements[name] = ty
	}
	return t
}

// objectElementTypes maps the common namespace's global elements whose type is
// an object type. Elements exist to find an object's type from its REST type, so
// these win a name collision, such as c:org (an object) against q:org (a filter
// clause). The type decides, as c:sequence is in no substitution group.
func (b *builder) objectElementTypes() map[string]string {
	types := map[string]string{}
	for _, n := range b.globalElements {
		if n.parent.attrs["targetNamespace"] != commonNS || n.attrs["type"] == "" {
			continue
		}
		if q := n.resolve(n.attrs["type"]); b.isObjectType(q) {
			types[n.attrs["name"]] = q.local
		}
	}
	return types
}

// isObjectType reports whether a type is c:ObjectType or derives from it.
func (b *builder) isObjectType(q qname) bool {
	seen := map[qname]bool{}
	for !seen[q] {
		if q == (qname{commonNS, "ObjectType"}) {
			return true
		}
		seen[q] = true
		def, ok := b.complex[q]
		if !ok {
			return false
		}
		next := qname{}
		for _, c := range def.children {
			if !c.is("complexContent") {
				continue
			}
			for _, d := range c.children {
				if d.is("extension") {
					next = d.resolve(d.attrs["base"])
				}
			}
		}
		q = next
	}
	return false
}

// elementTypes maps global element names to complex types. A name that two
// declarations give different types, or that is not always complex, is reported
// as conflicting: its element cannot be told from the local name alone.
func (b *builder) elementTypes() (map[string]string, map[string]bool) {
	types := map[string]string{}
	conflicting := map[string]bool{}
	for _, n := range b.globalElements {
		name := n.attrs["name"]
		it := b.elementItem(n, "element")
		if it.Kind != mpschema.KindComplex {
			conflicting[name] = true
			continue
		}
		if prev, seen := types[name]; seen && prev != it.Type {
			conflicting[name] = true
		}
		types[name] = it.Type
	}
	return types, conflicting
}

func allSameEnum(defs [][]string) bool {
	first := slices.Sorted(slices.Values(defs[0]))
	for _, d := range defs[1:] {
		if !slices.Equal(first, slices.Sorted(slices.Values(d))) {
			return false
		}
	}
	return true
}

func allSameType(defs []*flatType) bool {
	first := fingerprint(defs[0])
	for _, d := range defs[1:] {
		if !bytes.Equal(first, fingerprint(d)) {
			return false
		}
	}
	return true
}

func fingerprint(f *flatType) []byte {
	// Marshalling a map sorts its keys, and these values cannot fail to marshal.
	b, _ := json.Marshal(struct {
		Items map[string]mpschema.Item
		Open  bool
	}{f.items, f.open})
	return b
}

// flatten returns a complex type's keys, with those it inherits. name is the
// type's own name, the owner of any anonymous type declared inside it.
func (b *builder) flatten(n *node, name string) *flatType {
	if f, ok := b.flat[n]; ok {
		return f
	}
	f := &flatType{items: map[string]mpschema.Item{}}
	if b.visiting[n] {
		f.open = true
		return f
	}
	b.visiting[n] = true
	defer delete(b.visiting, n)

	_, raw := n.appinfo("rawType")
	if raw || name == "SearchFilterType" || name == "RawType" {
		f.open = true
	}
	b.addContent(f, n, name)
	b.flat[n] = f
	return f
}

// addContent adds what a complexType or extension node declares directly.
func (b *builder) addContent(f *flatType, n *node, owner string) {
	for _, c := range n.children {
		switch {
		case c.is("complexContent"):
			for _, d := range c.children {
				switch {
				case d.is("extension"):
					b.addBase(f, d, owner)
					b.addContent(f, d, owner)
				case d.is("restriction"):
					f.open = true // what it keeps of the base is not worked out
				}
			}
		case c.is("simpleContent"):
			f.open = true // a value with attributes: nothing to check by key
		case c.is("sequence"), c.is("choice"), c.is("all"), c.is("group"):
			b.addParticle(f, c, owner, false)
		case c.is("attribute"), c.is("attributeGroup"), c.is("anyAttribute"):
			b.addAttribute(f, c, owner)
		}
	}
}

func (b *builder) addBase(f *flatType, ext *node, owner string) {
	q := ext.resolve(ext.attrs["base"])
	base, ok := b.complex[q]
	if !ok {
		f.open = true
		return
	}
	bf := b.flatten(base, q.local)
	f.open = f.open || bf.open
	for key, it := range bf.items {
		f.add(key, it)
	}
}

// add records a key. A key seen twice can occur more than once; if the two
// declarations disagree on its type, nothing can be said of it.
func (f *flatType) add(key string, it mpschema.Item) {
	prev, seen := f.items[key]
	switch {
	case !seen:
		f.items[key] = it
	case prev.Type == it.Type && prev.Kind == it.Kind:
		prev.Multi = true
		f.items[key] = prev
	default:
		f.items[key] = mpschema.Item{Kind: mpschema.KindOpen, Multi: true}
	}
}

// addParticle adds a sequence, choice, all, group reference, element or any.
// multi says whether an enclosing particle repeats.
func (b *builder) addParticle(f *flatType, n *node, owner string, multi bool) {
	multi = multi || n.repeats()
	switch {
	case n.is("sequence"), n.is("choice"), n.is("all"):
		for _, c := range n.children {
			b.addParticle(f, c, owner, multi)
		}
	case n.is("any"):
		f.open = true
	case n.is("group"):
		q := n.resolve(n.attrs["ref"])
		g, ok := b.groups[q]
		if !ok || b.visiting[g] {
			f.open = true
			return
		}
		b.visiting[g] = true
		defer delete(b.visiting, g)
		for _, c := range g.children {
			b.addParticle(f, c, q.local, multi)
		}
	case n.is("element"):
		b.addElement(f, n, owner, multi)
	}
}

func (b *builder) addElement(f *flatType, n *node, owner string, multi bool) {
	ref, isRef := n.attrs["ref"]
	if !isRef {
		it := b.elementItem(n, owner)
		it.Multi = multi
		f.add(n.attrs["name"], it)
		return
	}
	q := n.resolve(ref)
	head, ok := b.elements[q]
	if !ok {
		f.add(q.local, mpschema.Item{Kind: mpschema.KindOpen, Multi: multi})
		return
	}
	it := b.elementItem(head, "element")
	it.Multi = multi || head.repeats()
	f.add(q.local, it)
	seen := map[qname]bool{q: true}
	queue := slices.Clone(b.members[q])
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if seen[m] {
			continue
		}
		seen[m] = true
		queue = append(queue, b.members[m]...)
		el, ok := b.elements[m]
		if !ok {
			continue
		}
		mi := b.elementItem(el, "element")
		mi.Multi = it.Multi || el.repeats()
		f.add(m.local, mi)
	}
}

func (b *builder) addAttribute(f *flatType, n *node, owner string) {
	switch {
	case n.is("anyAttribute"):
		f.open = true
	case n.is("attributeGroup"):
		g, ok := b.attrGroups[n.resolve(n.attrs["ref"])]
		if !ok || b.visiting[g] {
			f.open = true
			return
		}
		b.visiting[g] = true
		defer delete(b.visiting, g)
		for _, c := range g.children {
			if c.is("attribute") || c.is("attributeGroup") || c.is("anyAttribute") {
				b.addAttribute(f, c, owner)
			}
		}
	case n.attrs["ref"] != "":
		q := n.resolve(n.attrs["ref"])
		if decl, ok := b.attributes[q]; ok {
			f.add(q.local, b.elementItem(decl, "attribute"))
		} else {
			f.add(q.local, mpschema.Item{Kind: mpschema.KindOpen})
		}
	default:
		f.add(n.attrs["name"], b.elementItem(n, owner))
	}
}

// elementItem works out what an element or attribute declaration holds, in the
// namespace scope of its own file. Multi is left to the caller.
func (b *builder) elementItem(n *node, owner string) mpschema.Item {
	if _, raw := n.appinfo("rawType"); raw {
		return mpschema.Item{Kind: mpschema.KindOpen}
	}
	if t, ok := n.attrs["type"]; ok {
		return b.namedType(n.resolve(t))
	}
	name := owner + "." + n.attrs["name"]
	for _, c := range n.children {
		switch {
		case c.is("complexType"):
			if prev, ok := b.anonNames[c]; ok {
				name = prev
			} else {
				b.anonNames[c] = name
				b.complexDefs = append(b.complexDefs, namedDef{name, c})
			}
			return mpschema.Item{Type: name, Kind: mpschema.KindComplex}
		case c.is("simpleType"):
			return b.classifySimple(c, name)
		}
	}
	return mpschema.Item{Kind: mpschema.KindOpen}
}

func (b *builder) namedType(q qname) mpschema.Item {
	if q.ns == xsdNS {
		return builtin(q.local)
	}
	if _, ok := b.complex[q]; ok {
		return mpschema.Item{Type: q.local, Kind: mpschema.KindComplex}
	}
	if s, ok := b.simple[q]; ok {
		return b.classifySimple(s, q.local)
	}
	return mpschema.Item{Kind: mpschema.KindOpen}
}

func builtin(local string) mpschema.Item {
	switch local {
	case "boolean":
		return mpschema.Item{Kind: mpschema.KindBoolean}
	case "integer", "int", "long", "short", "byte",
		"nonNegativeInteger", "positiveInteger", "nonPositiveInteger", "negativeInteger",
		"unsignedInt", "unsignedLong", "unsignedShort", "unsignedByte":
		return mpschema.Item{Kind: mpschema.KindInteger}
	case "anyType":
		return mpschema.Item{Kind: mpschema.KindOpen}
	}
	return mpschema.Item{Kind: mpschema.KindSimple}
}

// classifySimple returns the item for a simple type and records its enumeration
// values under name. A name has several lists only when namespaces disagree.
func (b *builder) classifySimple(n *node, name string) mpschema.Item {
	it, values := b.simpleKind(n, map[*node]bool{})
	if it.Kind != mpschema.KindEnum {
		return it
	}
	if !slices.ContainsFunc(b.enums[name], func(v []string) bool { return slices.Equal(v, values) }) {
		b.enums[name] = append(b.enums[name], values)
	}
	return mpschema.Item{Type: name, Kind: mpschema.KindEnum}
}

// simpleKind works out a simple type's kind and enumeration values. Only a
// restriction can narrow a type: a union, a list and anything else are plain
// simple values. A restriction without facets of its own has its base's values.
func (b *builder) simpleKind(n *node, seen map[*node]bool) (mpschema.Item, []string) {
	plain := mpschema.Item{Kind: mpschema.KindSimple}
	if seen[n] {
		return plain, nil
	}
	seen[n] = true
	for _, c := range n.children {
		if !c.is("restriction") {
			continue
		}
		var values []string
		for _, facet := range c.children {
			if facet.is("enumeration") {
				values = append(values, facet.attrs["value"])
			}
		}
		if len(values) > 0 {
			return mpschema.Item{Kind: mpschema.KindEnum}, values
		}
		q := c.resolve(c.attrs["base"])
		if q.ns == xsdNS {
			return builtin(q.local), nil
		}
		if base, ok := b.simple[q]; ok {
			return b.simpleKind(base, seen)
		}
		return plain, nil
	}
	return plain, nil
}

// generate returns the gzipped JSON table of the XSD files below dir. The bytes
// depend on the input alone: map keys are sorted and gzip carries no mod time.
func generate(dir, line, source string) ([]byte, error) {
	t, err := parseDir(dir)
	if err != nil {
		return nil, err
	}
	t.Line = line
	t.Source = source
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
