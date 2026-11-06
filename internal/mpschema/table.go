// Package mpschema holds tables of the items midPoint accepts, generated from
// its XSD files, and a walker that checks configuration values against them.
package mpschema

import (
	"regexp"
	"strings"
)

// Kind says what an item holds.
type Kind string

const (
	KindComplex Kind = "complex"
	KindBoolean Kind = "boolean"
	KindInteger Kind = "integer"
	KindEnum    Kind = "enum"
	KindSimple  Kind = "simple"
	KindOpen    Kind = "open"
)

// Item is one key of a type.
type Item struct {
	Type  string // complex type name, or enum name in Table.Enums; empty otherwise
	Kind  Kind
	Multi bool
}

// Type is a complex type with its keys flattened: inherited keys and
// substitution-group members are included.
type Type struct {
	Items map[string]Item
	Open  bool
}

// Table describes one midPoint line.
type Table struct {
	Line     string // "4.9"
	Source   string // image reference with digest
	Types    map[string]*Type
	Enums    map[string][]string
	Elements map[string]string // global element local name -> complex type
}

// For returns the table for a server version. No tables are loaded yet.
func For(serverVersion string) (*Table, bool) {
	if _, ok := line(serverVersion); !ok {
		return nil, false
	}
	return nil, false
}

var lineRe = regexp.MustCompile(`^(\d+)\.(\d+)`)

// line returns the leading major.minor of a version, whatever follows it.
func line(version string) (string, bool) {
	m := lineRe.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2], true
}

// Descend follows items from the root type and returns the complex type
// reached. It reports false when an item is missing or holds no complex type.
func (t *Table) Descend(root string, items ...string) (string, bool) {
	cur := root
	if _, ok := t.Types[cur]; !ok {
		return "", false
	}
	for _, name := range items {
		ty, ok := t.Types[cur]
		if !ok {
			return "", false
		}
		it, ok := ty.Items[name]
		if !ok || it.Kind != KindComplex || it.Type == "" {
			return "", false
		}
		cur = it.Type
	}
	return cur, true
}
