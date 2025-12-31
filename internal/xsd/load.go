package xsd

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Set is a group of schema documents loaded together: the roots the caller
// asked for plus everything they pull in through xs:include and xs:import.
// Generation works over a whole Set, because a type in one document routinely
// refers to a type in another.
type Set struct {
	// Roots are the documents named by the caller, in the order given.
	Roots []*Schema
	// All is every document in the set, roots first, then dependencies in the
	// order they were discovered.
	All []*Schema
	// Missing records schemaLocations that could not be read. A schema that
	// imports a namespace it does not ship (xhtml, xlink) is common and is not
	// fatal; the resolver treats the unknown types as strings and the caller
	// can report these.
	Missing []string

	byPath map[string]*Schema
}

// Load reads the named schema documents and everything they reference.
func Load(paths ...string) (*Set, error) {
	s := &Set{byPath: map[string]*Schema{}}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		doc, err := s.load(abs)
		if err != nil {
			return nil, err
		}
		if doc != nil {
			s.Roots = append(s.Roots, doc)
		}
	}
	if len(s.All) == 0 {
		return nil, fmt.Errorf("no schema documents loaded")
	}
	return s, nil
}

// Parse decodes a single schema document from bytes. location is recorded on
// the result and used to resolve relative references; it may be empty.
func Parse(data []byte, location string) (*Schema, error) {
	var doc Schema
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", location, err)
	}
	if doc.XMLName.Local != "schema" {
		return nil, fmt.Errorf("%s: root element is %q, not xs:schema", location, doc.XMLName.Local)
	}
	doc.Location = location
	doc.Prefixes = prefixes(data)
	return &doc, nil
}

// load reads one document by absolute path, returning the already-loaded copy
// if the path has been seen before.
func (s *Set) load(abs string) (*Schema, error) {
	if doc, ok := s.byPath[abs]; ok {
		return doc, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	doc, err := Parse(data, abs)
	if err != nil {
		return nil, err
	}
	// Registered before the references are followed, so that a circular
	// include terminates.
	s.byPath[abs] = doc
	s.All = append(s.All, doc)

	dir := filepath.Dir(abs)
	refs := make([]string, 0, len(doc.Includes)+len(doc.Imports)+len(doc.Redefines))
	for _, inc := range doc.Includes {
		refs = append(refs, inc.SchemaLocation)
	}
	for _, inc := range doc.Redefines {
		refs = append(refs, inc.SchemaLocation)
	}
	for _, imp := range doc.Imports {
		refs = append(refs, imp.SchemaLocation)
	}
	for _, ref := range refs {
		if ref == "" || strings.Contains(ref, "://") {
			// An import with no schemaLocation, or one pointing at the network.
			// Neither is fetched: generation is an offline, reproducible step.
			if ref != "" {
				s.Missing = append(s.Missing, ref)
			}
			continue
		}
		target := ref
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, filepath.FromSlash(ref))
		}
		if _, err := s.load(target); err != nil {
			if os.IsNotExist(err) {
				s.Missing = append(s.Missing, ref)
				continue
			}
			return nil, err
		}
	}
	return doc, nil
}

// prefixes pulls the xmlns declarations off the root element. encoding/xml
// resolves namespaces for element and attribute names but discards the prefix
// table, and QName-valued attributes (type="tns:Foo", base="xs:string") carry
// prefixes that only that table can resolve.
func prefixes(data []byte) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		for _, a := range start.Attr {
			switch {
			case a.Name.Space == "xmlns":
				out[a.Name.Local] = a.Value
			case a.Name.Space == "" && a.Name.Local == "xmlns":
				out[""] = a.Value
			}
		}
		return out
	}
}

// Doc returns the annotation text as a single collapsed string, or "".
func (a *Annotation) Doc() string {
	if a == nil {
		return ""
	}
	var parts []string
	for _, d := range a.Documentation {
		if d = collapse(d); d != "" {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, " ")
}

// collapse turns the arbitrary indentation inside an xs:documentation body
// into a single space-separated line.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
