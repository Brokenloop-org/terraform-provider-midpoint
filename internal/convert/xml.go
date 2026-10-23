package convert

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// decodeXML decodes a midPoint XML object to the map shape of its JSON form:
// {"role": {...}}. Namespaces are ignored. The attributes oid and id become
// the keys "oid" and "@id", other attributes keep their local name (xsi:type
// becomes "@type"), repeated sibling elements become a list, and leaf
// elements become strings: XML carries no types, so a number or boolean is
// the string "3" or "true".
func decodeXML(data []byte) (map[string]any, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if start, ok := tok.(xml.StartElement); ok {
			v, err := decodeElement(dec, start)
			if err != nil {
				return nil, err
			}
			if _, isMap := v.(map[string]any); !isMap {
				return nil, errors.New("root element is empty")
			}
			return map[string]any{start.Name.Local: v}, nil
		}
	}
}

func decodeElement(dec *xml.Decoder, start xml.StartElement) (any, error) {
	m := map[string]any{}
	for _, a := range start.Attr {
		switch {
		case a.Name.Space == "xmlns" || a.Name.Local == "xmlns":
		case a.Name.Local == "id" && a.Name.Space == "":
			m["@id"] = a.Value
		case a.Name.Local == "type" && a.Name.Space != "":
			m["@type"] = a.Value
		default:
			m[a.Name.Local] = a.Value
		}
	}
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := decodeElement(dec, t)
			if err != nil {
				return nil, err
			}
			switch prev := m[t.Name.Local].(type) {
			case nil:
				m[t.Name.Local] = child
			case []any:
				m[t.Name.Local] = append(prev, child)
			default:
				m[t.Name.Local] = []any{prev, child}
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			s := strings.TrimSpace(text.String())
			switch {
			case len(m) == 0:
				return s, nil
			case s != "":
				return nil, fmt.Errorf("element %s mixes text and attributes or children", start.Name.Local)
			}
			return m, nil
		}
	}
}
