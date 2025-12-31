// Package infer derives an XML Schema from example documents. What it produces
// is a description of the samples, not of the format: a schema inferred from
// three purchase orders will reject the fourth if that one uses an element the
// samples never showed. It is a starting point to edit, and the generated
// schema says so in its own annotations.
package infer

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Options controls inference.
type Options struct {
	// TargetNamespace overrides the namespace taken from the samples.
	TargetNamespace string
	// MaxEnum is the largest number of distinct values a field may have before
	// it stops being treated as an enumeration. Zero disables enumeration
	// inference entirely.
	MaxEnum int
	// MinEnumSamples is how many values must have been seen before an
	// enumeration is trusted. Three documents that happen to share a value
	// prove nothing; a hundred that do are worth encoding.
	MinEnumSamples int
	// Strings suppresses datatype inference, giving every value xs:string.
	// Useful when the samples are small enough that an inferred xs:int would
	// be a guess about the format rather than a fact about the data.
	Strings bool
}

// DefaultOptions are the settings the command line starts from.
func DefaultOptions() Options {
	return Options{MaxEnum: 12, MinEnumSamples: 8}
}

// Schema is the accumulated structure of every document seen so far.
type Schema struct {
	opts Options

	// elements holds one merged description per element name. Merging by name
	// -- rather than by the path an element was found at -- is what keeps the
	// output to one complex type per element instead of one per position, and
	// it matches how schemas are written by hand.
	elements map[xml.Name]*Element
	// order is the discovery order of elements, so the emitted schema is
	// stable across runs.
	order []xml.Name
	// roots are the document elements, in discovery order.
	roots    []xml.Name
	rootSeen map[xml.Name]bool
	// namespaces counts the namespaces seen, to choose a target namespace.
	namespaces map[string]int
	// Warnings records what could not be represented faithfully.
	Warnings []string
	// names memoizes the generated type name of each element.
	names map[xml.Name]string
	// docs counts the documents folded in, for the schema's own annotation.
	docs int
}

// Element is the merged description of every occurrence of one element name.
type Element struct {
	Name xml.Name
	// Instances is how many times the element was seen.
	Instances int

	Attrs     map[xml.Name]*Attr
	AttrOrder []xml.Name

	Children   map[xml.Name]*Child
	ChildOrder []xml.Name
	// OrderVaries is set when two instances presented their children in
	// incompatible orders, which rules out xs:sequence.
	OrderVaries bool

	// TextInstances counts occurrences with non-whitespace text content.
	TextInstances int
	Text          *Values
	// Empty counts occurrences with neither children nor text, which is what
	// makes an element's content optional.
	Empty int
}

// Child is one element name seen inside a parent.
type Child struct {
	Name xml.Name
	// ParentsWith is the number of parent instances that contained it at least
	// once; comparing it with the parent's instance count gives minOccurs.
	ParentsWith int
	// MaxPerParent is the most occurrences seen inside a single parent, which
	// gives maxOccurs.
	MaxPerParent int
}

// Attr is one attribute seen on an element.
type Attr struct {
	Name   xml.Name
	Count  int
	Values *Values
}

// New starts an empty schema.
func New(opts Options) *Schema {
	return &Schema{
		opts:       opts,
		elements:   map[xml.Name]*Element{},
		rootSeen:   map[xml.Name]bool{},
		namespaces: map[string]int{},
	}
}

// AddFile folds one document into the schema.
func (s *Schema) AddFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := s.Add(f); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Add folds one document, read from r, into the schema.
func (s *Schema) Add(r io.Reader) error {
	s.docs++
	dec := xml.NewDecoder(r)
	// Documents in the wild are not always UTF-8 and not always well-behaved
	// about entities; neither should stop an inference run.
	dec.Strict = false
	dec.CharsetReader = charsetPassthrough

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !s.rootSeen[start.Name] {
			s.rootSeen[start.Name] = true
			s.roots = append(s.roots, start.Name)
		}
		if err := s.walk(dec, start); err != nil {
			return err
		}
	}
}

// walk consumes one element and everything inside it, folding what it finds
// into the merged description of that element name.
func (s *Schema) walk(dec *xml.Decoder, start xml.StartElement) error {
	el := s.element(start.Name)
	el.Instances++

	for _, a := range start.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			s.namespaces[a.Value]++
			continue
		}
		if a.Name.Space == xsiNamespace {
			// xsi:type, xsi:nil and the schema-location hints describe the
			// document's relationship to a schema; they are not content.
			continue
		}
		el.attr(a).Values.observe(a.Value)
	}

	// Counted per instance, so that maxOccurs reflects the most any single
	// parent held rather than the total across the corpus.
	counts := map[xml.Name]int{}
	var seenOrder []xml.Name
	var text strings.Builder

	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if counts[t.Name] == 0 {
				seenOrder = append(seenOrder, t.Name)
			}
			counts[t.Name]++
			el.child(t.Name)
			if err := s.walk(dec, t); err != nil {
				return err
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			s.finish(el, counts, seenOrder, text.String())
			return nil
		}
	}
}

// finish folds one completed instance into the merged element.
func (s *Schema) finish(el *Element, counts map[xml.Name]int, seenOrder []xml.Name, text string) {
	for name, n := range counts {
		c := el.Children[name]
		c.ParentsWith++
		if n > c.MaxPerParent {
			c.MaxPerParent = n
		}
	}
	if !el.OrderVaries && !isSubsequence(seenOrder, el.ChildOrder) {
		el.OrderVaries = true
	}
	trimmed := strings.TrimSpace(text)
	if trimmed != "" {
		el.TextInstances++
		el.Text.observe(trimmed)
	}
	if trimmed == "" && len(counts) == 0 {
		el.Empty++
	}
	s.namespaces[el.Name.Space]++
}

// element returns the merged description for a name, creating it on first use.
func (s *Schema) element(name xml.Name) *Element {
	if el, ok := s.elements[name]; ok {
		return el
	}
	el := &Element{
		Name:     name,
		Attrs:    map[xml.Name]*Attr{},
		Children: map[xml.Name]*Child{},
		Text:     newValues(),
	}
	s.elements[name] = el
	s.order = append(s.order, name)
	return el
}

func (e *Element) attr(a xml.Attr) *Attr {
	at, ok := e.Attrs[a.Name]
	if !ok {
		at = &Attr{Name: a.Name, Values: newValues()}
		e.Attrs[a.Name] = at
		e.AttrOrder = append(e.AttrOrder, a.Name)
	}
	at.Count++
	return at
}

func (e *Element) child(name xml.Name) *Child {
	c, ok := e.Children[name]
	if !ok {
		c = &Child{Name: name}
		e.Children[name] = c
		e.ChildOrder = append(e.ChildOrder, name)
	}
	return c
}

// isSubsequence reports whether the names in got appear in want in the same
// relative order. A document that presents a, c is consistent with the order
// a, b, c; one that presents c, a is not.
func isSubsequence(got, want []xml.Name) bool {
	i := 0
	for _, g := range got {
		found := false
		for ; i < len(want); i++ {
			if want[i] == g {
				i++
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// warn records an approximation.
func (s *Schema) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, existing := range s.Warnings {
		if existing == msg {
			return
		}
	}
	s.Warnings = append(s.Warnings, msg)
}

// targetNamespace picks the namespace for the emitted schema: the option if
// given, otherwise the namespace of the first document root.
func (s *Schema) targetNamespace() string {
	if s.opts.TargetNamespace != "" {
		return s.opts.TargetNamespace
	}
	if len(s.roots) > 0 {
		return s.roots[0].Space
	}
	return ""
}

// otherNamespaces lists namespaces present in the samples that the emitted
// schema cannot describe, because one schema document has exactly one target
// namespace.
func (s *Schema) otherNamespaces() []string {
	target := s.targetNamespace()
	var out []string
	for _, name := range s.order {
		if name.Space != "" && name.Space != target && name.Space != xsiNamespace {
			out = append(out, name.Space)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(in []string) []string {
	var out []string
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

const xsiNamespace = "http://www.w3.org/2001/XMLSchema-instance"

// charsetPassthrough accepts any declared encoding by reading the bytes as
// they are. Refusing a document over its encoding declaration would fail the
// common case -- a windows-1252 file that is ASCII in practice -- for no gain.
func charsetPassthrough(charset string, input io.Reader) (io.Reader, error) {
	return input, nil
}
