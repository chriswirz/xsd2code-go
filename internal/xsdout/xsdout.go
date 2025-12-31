// Package xsdout writes a resolved model back out as an XML Schema document.
//
// It is the reverse of internal/xsd plus internal/ir, and it exists so that a
// model built from something other than a schema -- a Postgres database, today
// -- can be handed to anything that consumes XSD, this tool's own generators
// included.
package xsdout

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// Options controls the emitted document.
type Options struct {
	// TargetNamespace overrides the model's own namespace.
	TargetNamespace string
	// Header is a comment placed at the top of the document, saying where the
	// schema came from. Empty writes no comment.
	Header []string
}

// Write renders the model as a schema document.
func Write(m *ir.Model, o Options) string {
	target := o.TargetNamespace
	if target == "" {
		target = m.TargetNamespace
	}
	w := &writer{pad: "  "}

	w.line(`<?xml version="1.0" encoding="UTF-8"?>`)
	if len(o.Header) > 0 {
		w.line(`<!--`)
		for _, line := range o.Header {
			w.line(`  %s`, escape(line))
		}
		w.line(`-->`)
	}

	attrs := []string{`xmlns:xs="http://www.w3.org/2001/XMLSchema"`}
	if target != "" {
		attrs = append(attrs,
			`targetNamespace=`+attrv(target),
			`xmlns:tns=`+attrv(target),
			`elementFormDefault="qualified"`)
	}
	w.line(`<xs:schema %s>`, strings.Join(attrs, " "))
	w.in()

	// Roots first: the document elements are what a reader looks for, and what
	// a validator needs to find.
	for _, root := range m.Roots {
		w.line("")
		w.writeRoot(m, root, target)
	}
	for _, t := range m.Types {
		w.line("")
		switch t.Kind {
		case ir.Enum:
			w.writeEnum(t)
		default:
			w.writeComplex(m, t, target)
		}
	}

	w.out()
	w.line(`</xs:schema>`)
	return w.String()
}

// writeRoot declares one global element.
func (w *writer) writeRoot(m *ir.Model, root *ir.Root, target string) {
	typ := root.Type
	if typ == "" {
		typ = "xs:" + root.Builtin.String()
	} else {
		typ = qualify(typ, target)
	}
	if root.Doc == "" {
		w.line(`<xs:element name=%s type=%s/>`, attrv(root.XMLName), attrv(typ))
		return
	}
	w.line(`<xs:element name=%s type=%s>`, attrv(root.XMLName), attrv(typ))
	w.in()
	w.annotate(root.Doc)
	w.out()
	w.line(`</xs:element>`)
}

// writeEnum declares a named simple type restricted to its values.
func (w *writer) writeEnum(t *ir.Type) {
	w.line(`<xs:simpleType name=%s>`, attrv(t.Name))
	w.in()
	w.annotate(t.Doc)
	w.line(`<xs:restriction base="xs:%s">`, t.BaseBuiltin.String())
	w.in()
	for _, v := range t.Values {
		if v.Doc == "" {
			w.line(`<xs:enumeration value=%s/>`, attrv(v.Value))
			continue
		}
		w.line(`<xs:enumeration value=%s>`, attrv(v.Value))
		w.in()
		w.annotate(v.Doc)
		w.out()
		w.line(`</xs:enumeration>`)
	}
	w.out()
	w.line(`</xs:restriction>`)
	w.out()
	w.line(`</xs:simpleType>`)
}

// writeComplex declares one complex type, in whichever of the shapes its
// content calls for.
func (w *writer) writeComplex(m *ir.Model, t *ir.Type, target string) {
	var attrs []string
	attrs = append(attrs, `name=`+attrv(t.Name))
	if t.Abstract {
		attrs = append(attrs, `abstract="true"`)
	}
	if t.Mixed {
		attrs = append(attrs, `mixed="true"`)
	}
	w.line(`<xs:complexType %s>`, strings.Join(attrs, " "))
	w.in()
	w.annotate(t.Doc)

	elements, attributes, text := partition(t)

	switch {
	case t.Base != "":
		// Derivation by extension: the base carries its own members, and only
		// what this type adds is declared here.
		w.line(`<xs:complexContent>`)
		w.in()
		w.line(`<xs:extension base=%s>`, attrv(qualify(t.Base, target)))
		w.in()
		w.writeParticles(m, elements, target)
		w.writeAttributes(m, attributes, target)
		w.out()
		w.line(`</xs:extension>`)
		w.out()
		w.line(`</xs:complexContent>`)
	case text != nil && len(elements) == 0:
		// A value with attributes on it.
		w.line(`<xs:simpleContent>`)
		w.in()
		w.line(`<xs:extension base=%s>`, attrv(scalarType(m, text, target)))
		w.in()
		w.writeAttributes(m, attributes, target)
		w.out()
		w.line(`</xs:extension>`)
		w.out()
		w.line(`</xs:simpleContent>`)
	default:
		w.writeParticles(m, elements, target)
		w.writeAttributes(m, attributes, target)
	}

	w.out()
	w.line(`</xs:complexType>`)
}

// writeParticles writes the sequence of child elements.
func (w *writer) writeParticles(m *ir.Model, elements []*ir.Field, target string) {
	if len(elements) == 0 {
		return
	}
	w.line(`<xs:sequence>`)
	w.in()
	for _, f := range elements {
		if f.Origin == ir.AnyField {
			w.line(`<xs:any processContents="lax"%s/>`, occurs(f))
			continue
		}
		w.writeElement(m, f, target)
	}
	w.out()
	w.line(`</xs:sequence>`)
}

// writeElement writes one child element declaration.
func (w *writer) writeElement(m *ir.Model, f *ir.Field, target string) {
	var attrs []string
	attrs = append(attrs, `name=`+attrv(f.XMLName))
	inlineList := f.List && f.TypeName == ""
	if !inlineList {
		attrs = append(attrs, `type=`+attrv(scalarType(m, f, target)))
	}
	head := strings.Join(attrs, " ") + occurs(f)
	if f.Nillable {
		head += ` nillable="true"`
	}
	if f.Fixed != "" {
		head += ` fixed=` + attrv(f.Fixed)
	} else if f.Default != "" {
		head += ` default=` + attrv(f.Default)
	}

	if !inlineList && f.Doc == "" {
		w.line(`<xs:element %s/>`, head)
		return
	}
	w.line(`<xs:element %s>`, head)
	w.in()
	w.annotate(f.Doc)
	if inlineList {
		// An xs:list has no named type here, so it is declared in place.
		w.line(`<xs:simpleType>`)
		w.in()
		w.line(`<xs:list itemType="xs:%s"/>`, f.Builtin.String())
		w.out()
		w.line(`</xs:simpleType>`)
	}
	w.out()
	w.line(`</xs:element>`)
}

// writeAttributes writes the attribute declarations, and the wildcard if the
// type has one.
func (w *writer) writeAttributes(m *ir.Model, attributes []*ir.Field, target string) {
	for _, f := range attributes {
		if f.Origin == ir.AnyAttrField {
			w.line(`<xs:anyAttribute processContents="lax"/>`)
			continue
		}
		head := `name=` + attrv(f.XMLName) + ` type=` + attrv(scalarType(m, f, target))
		if !f.Optional {
			head += ` use="required"`
		}
		if f.Fixed != "" {
			head += ` fixed=` + attrv(f.Fixed)
		} else if f.Default != "" {
			head += ` default=` + attrv(f.Default)
		}
		if f.Doc == "" {
			w.line(`<xs:attribute %s/>`, head)
			continue
		}
		w.line(`<xs:attribute %s>`, head)
		w.in()
		w.annotate(f.Doc)
		w.out()
		w.line(`</xs:attribute>`)
	}
}

// partition splits a type's fields by where they live in a document, because
// XSD wants them in that order: elements inside the compositor, attributes
// after it, and the text value in place of both.
func partition(t *ir.Type) (elements, attributes []*ir.Field, text *ir.Field) {
	for _, f := range t.Fields {
		switch f.Origin {
		case ir.AttributeField, ir.AnyAttrField:
			attributes = append(attributes, f)
		case ir.TextField:
			text = f
		default:
			elements = append(elements, f)
		}
	}
	return elements, attributes, text
}

// scalarType renders the type reference of a field.
func scalarType(m *ir.Model, f *ir.Field, target string) string {
	if f.TypeName != "" {
		return qualify(f.TypeName, target)
	}
	return "xs:" + f.Builtin.String()
}

// occurs renders minOccurs and maxOccurs, writing neither when the field is
// the exactly-once default.
func occurs(f *ir.Field) string {
	out := ""
	if f.Optional || f.Repeated {
		// A repeated field is modelled as optional-and-unbounded: the model
		// records "how many", not "at least one", and an empty collection has
		// to remain expressible.
		out += ` minOccurs="0"`
	}
	if f.Repeated {
		out += ` maxOccurs="unbounded"`
	}
	return out
}

// qualify prefixes a generated type name for the target namespace.
func qualify(name, target string) string {
	if target == "" {
		return name
	}
	return "tns:" + name
}

// annotate writes an xs:documentation block, or nothing.
func (w *writer) annotate(text string) {
	if text == "" {
		return
	}
	w.line(`<xs:annotation>`)
	w.in()
	w.line(`<xs:documentation>%s</xs:documentation>`, escape(text))
	w.out()
	w.line(`</xs:annotation>`)
}

// writer is a small indent-aware text writer.
type writer struct {
	sb    strings.Builder
	depth int
	pad   string
}

func (w *writer) in()  { w.depth++ }
func (w *writer) out() { w.depth-- }

func (w *writer) line(format string, args ...any) {
	if format != "" {
		w.sb.WriteString(strings.Repeat(w.pad, w.depth))
		if len(args) == 0 {
			w.sb.WriteString(format)
		} else {
			fmt.Fprintf(&w.sb, format, args...)
		}
	}
	w.sb.WriteString("\n")
}

func (w *writer) String() string { return w.sb.String() }

// escape makes text safe as character data.
func escape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// attrv renders a quoted, XML-escaped attribute value.
func attrv(s string) string { return `"` + escape(s) + `"` }
