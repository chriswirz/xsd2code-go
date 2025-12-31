package gen

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genPython emits dataclasses and the readers that fill them from
// xml.etree.ElementTree, which is in the standard library, so the generated
// module has no dependencies.
func genPython(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	b := newBuf("    ")
	for _, line := range header(o, "#") {
		b.L("%s", line)
	}
	b.L("")
	b.L(`"""Data classes for the schema, and the readers that build them."""`)
	b.L("")
	b.L("from __future__ import annotations")
	b.L("")
	b.L("import xml.etree.ElementTree as ET")
	b.L("from dataclasses import dataclass, field")
	b.L("from enum import Enum")
	b.L("from typing import Optional")
	b.L("")
	if ns := m.TargetNamespace; ns != "" {
		b.doc("# ", "The namespace every element of this schema lives in.")
		b.L("NAMESPACE = %s", pyString(ns))
	} else {
		b.doc("# ", "This schema is unqualified: its elements have no namespace.")
		b.L("NAMESPACE = None")
	}
	b.L("")
	b.L("%s", pythonRuntime)

	// Annotations are lazy under "from __future__ import annotations", so a
	// field may name a type declared later; the readers come after every type
	// regardless, because they call one another.
	// A class statement evaluates its base at once, so a derived type cannot
	// be written before the type it extends.
	types := baseFirst(m)
	for _, t := range types {
		b.L("")
		if t.Kind == ir.Enum {
			genPythonEnum(b, t)
		} else {
			genPythonClass(b, m, rel, t)
		}
	}
	for _, t := range types {
		if t.Kind == ir.Enum {
			continue
		}
		b.L("")
		genPythonReader(b, m, t)
	}
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		b.L("")
		genPythonRoot(b, root)
	}
	return []File{{Name: "models.py", Content: b.Bytes()}}, nil
}

// genPythonEnum writes a str-valued Enum, so a member compares equal to the
// lexical value the document carried and serializes without conversion.
func genPythonEnum(b *buf, t *ir.Type) {
	b.L("class %s(str, Enum):", t.Name)
	b.In()
	b.L(`"""%s"""`, pyDocText(firstNonEmpty(t.Doc, fmt.Sprintf("The %q enumeration.", t.XMLName))))
	b.L("")
	names := newNames()
	for _, v := range t.Values {
		if v.Doc != "" {
			b.doc("# ", v.Doc)
		}
		b.L("%s = %s", names.take(pyEnumName(v)), pyString(v.Value))
	}
	if len(t.Values) == 0 {
		b.L("pass")
	}
	b.Out()
}

// genPythonClass writes one dataclass.
func genPythonClass(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type) {
	b.L("@dataclass")
	decl := "class " + t.Name
	if t.Base != "" {
		decl += "(" + t.Base + ")"
	}
	b.L("%s:", decl)
	b.In()
	b.L(`"""%s"""`, pyDocText(firstNonEmpty(t.Doc, fmt.Sprintf("The %q complex type.", t.XMLName))))
	if rel != nil {
		if tbl := rel.Table(t.Name); tbl != nil {
			b.L("")
			b.L("#: The table this type is stored in.")
			b.L("TABLE = %s", pyString(tbl.Name))
			b.L("#: Each field's column, for a caller assembling its own SQL.")
			b.L("COLUMNS = {")
			b.In()
			names := newNames()
			for _, f := range t.Fields {
				if col := columnObj(tbl, f); col != nil {
					b.L("%s: %s,", pyString(pyField(names.take(ir.Snake(f.Name)))), pyString(col.Name))
				}
			}
			b.Out()
			b.L("}")
		}
	}
	b.L("")

	if len(t.Fields) == 0 {
		b.L("pass")
		b.Out()
		return
	}
	// Every field gets a default. A dataclass cannot put a defaulted field
	// before an undefaulted one, and inheritance makes that unavoidable as
	// soon as a base has any default at all.
	names := newNames()
	for _, f := range t.Fields {
		if d := fieldDoc(t, f); d != "" {
			b.doc("# ", d)
		}
		b.L("%s: %s = %s", pyField(names.take(ir.Snake(f.Name))), pyType(m, f), pyDefault(m, f))
	}
	b.Out()
}

// genPythonReader writes the function that builds one object from an element.
func genPythonReader(b *buf, m *ir.Model, t *ir.Type) {
	b.L("def read_%s(el: ET.Element) -> %s:", ir.Snake(t.Name), t.Name)
	b.In()
	b.L(`"""Reads a %s from the element that contains it."""`, t.Name)
	b.L("return %s(", t.Name)
	b.In()
	if t.Base != "" {
		// The base's own members are read by its reader, so the two cannot
		// disagree about how a field is decoded.
		b.L("**vars(read_%s(el)),", ir.Snake(t.Base))
	}
	names := newNames()
	for _, f := range t.Fields {
		b.L("%s=%s,", pyField(names.take(ir.Snake(f.Name))), pyRead(m, f))
	}
	b.Out()
	b.L(")")
	b.Out()
}

// genPythonRoot writes the document entry points.
func genPythonRoot(b *buf, root *ir.Root) {
	name := ir.Snake(ir.Pascal(root.XMLName))
	doc := fmt.Sprintf("Parses a %q document.", root.XMLName)
	if root.Doc != "" {
		doc += " " + root.Doc
	}
	b.L("def parse_%s(xml: str) -> %s:", name, root.Type)
	b.In()
	b.L(`"""%s"""`, pyDocText(doc))
	b.L("return _read_root_%s(ET.fromstring(xml))", name)
	b.Out()
	b.L("")
	b.L("")
	b.L("def load_%s(path: str) -> %s:", name, root.Type)
	b.In()
	b.L(`"""Parses a %s document from a file."""`, root.XMLName)
	b.L("return _read_root_%s(ET.parse(path).getroot())", name)
	b.Out()
	b.L("")
	b.L("")
	b.L("def _read_root_%s(el: ET.Element) -> %s:", name, root.Type)
	b.In()
	b.L("if _local(el.tag) != %s:", pyString(root.XMLName))
	b.In()
	b.L(`raise ValueError(f"expected a <%s> document, got <{_local(el.tag)}>")`, root.XMLName)
	b.Out()
	b.L("return read_%s(el)", ir.Snake(root.Type))
	b.Out()
}

// pyType renders a field's annotation.
func pyType(m *ir.Model, f *ir.Field) string {
	switch f.Origin {
	case ir.AnyField:
		return "list[ET.Element]"
	case ir.AnyAttrField:
		return "dict[str, str]"
	}
	base := f.TypeName
	if base == "" {
		base = pyBuiltin(f.Builtin)
	}
	switch {
	case f.Repeated, f.List:
		return "list[" + base + "]"
	case f.Optional:
		return "Optional[" + base + "]"
	}
	return base
}

func pyBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "bool"
	case ir.Byte, ir.Short, ir.Int, ir.Long, ir.UnsignedByte, ir.UnsignedShort,
		ir.UnsignedInt, ir.UnsignedLong:
		return "int"
	case ir.Float, ir.Double:
		return "float"
	case ir.Decimal:
		// Python's Decimal would be exact, but it would also make every value
		// a Decimal in code that mostly wants to print it. The lexical string
		// round-trips, and decimal.Decimal(value) is one call away.
		return "str"
	}
	return "str"
}

// pyDefault renders the default a dataclass field is declared with.
func pyDefault(m *ir.Model, f *ir.Field) string {
	switch {
	case f.Origin == ir.AnyAttrField:
		return "field(default_factory=dict)"
	case f.Repeated, f.List, f.Origin == ir.AnyField:
		// A mutable default has to be a factory, or every instance would share
		// one list.
		return "field(default_factory=list)"
	case f.Optional:
		return "None"
	}
	switch pyType(m, f) {
	case "int":
		return "0"
	case "float":
		return "0.0"
	case "bool":
		return "False"
	case "str":
		return `""`
	}
	return "None"
}

// pyRead renders the expression that decodes one field.
func pyRead(m *ir.Model, f *ir.Field) string {
	ns := "NAMESPACE"
	if f.Namespace == "" {
		ns = "None"
	}
	conv := pyConvert(m, f)

	switch f.Origin {
	case ir.AnyAttrField:
		return "dict(el.attrib)"
	case ir.AnyField:
		return "list(el)"
	case ir.TextField:
		return pyApply(conv, "_text(el)")
	case ir.AttributeField:
		source := fmt.Sprintf("_attr(el, %s, %s)", ns, pyString(f.XMLName))
		if f.List {
			return fmt.Sprintf("[%s for v in _split(%s)]", pyApply(conv, "v"), source)
		}
		if f.Optional {
			if conv == "" {
				return source
			}
			return fmt.Sprintf("_opt(%s, %s)", source, conv)
		}
		return pyApply(conv, fmt.Sprintf("_require_attr(el, %s, %s)", ns, pyString(f.XMLName)))
	}

	// A child element.
	if f.Repeated {
		if f.TypeName != "" && isClass(m, f.TypeName) {
			return fmt.Sprintf("[read_%s(c) for c in _children(el, %s, %s)]",
				ir.Snake(f.TypeName), ns, pyString(f.XMLName))
		}
		return fmt.Sprintf("[%s for c in _children(el, %s, %s)]",
			pyApply(conv, "_text(c)"), ns, pyString(f.XMLName))
	}
	if f.List {
		return fmt.Sprintf("[%s for v in _split(_child_text(el, %s, %s) or \"\")]",
			pyApply(conv, "v"), ns, pyString(f.XMLName))
	}
	if f.TypeName != "" && isClass(m, f.TypeName) {
		child := fmt.Sprintf("_child(el, %s, %s)", ns, pyString(f.XMLName))
		if f.Optional {
			return fmt.Sprintf("_opt(%s, read_%s)", child, ir.Snake(f.TypeName))
		}
		return fmt.Sprintf("read_%s(_require(%s, %s))", ir.Snake(f.TypeName), child, pyString(f.XMLName))
	}
	source := fmt.Sprintf("_child_text(el, %s, %s)", ns, pyString(f.XMLName))
	if f.Optional {
		if conv == "" {
			return source
		}
		return fmt.Sprintf("_opt(%s, %s)", source, conv)
	}
	return pyApply(conv, fmt.Sprintf("_require_text(el, %s, %s)", ns, pyString(f.XMLName)))
}

// pyApply wraps an expression in a conversion, or leaves it alone.
func pyApply(conv, source string) string {
	if conv == "" {
		return source
	}
	return conv + "(" + source + ")"
}

// pyConvert names the callable that converts a lexical value, or "" when the
// string is already the value.
func pyConvert(m *ir.Model, f *ir.Field) string {
	if f.TypeName != "" {
		if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum {
			return f.TypeName
		}
		return ""
	}
	switch f.Builtin {
	case ir.Bool:
		return "_bool"
	case ir.Byte, ir.Short, ir.Int, ir.Long, ir.UnsignedByte, ir.UnsignedShort,
		ir.UnsignedInt, ir.UnsignedLong:
		return "int"
	case ir.Float, ir.Double:
		return "float"
	}
	return ""
}

// pyEnumName renders an enum member name in the upper case Python constants
// conventionally use.
func pyEnumName(v ir.EnumValue) string {
	name := ir.ScreamingSnake(v.Value)
	if name == "" {
		name = ir.ScreamingSnake(v.Name)
	}
	if name == "" {
		return "VALUE"
	}
	return name
}

// pyField suffixes a name that is a Python keyword or would shadow a builtin
// that generated code uses.
func pyField(name string) string {
	if name == "" {
		return "value"
	}
	if pyKeywords[name] {
		return name + "_"
	}
	return name
}

var pyKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true,
	"assert": true, "async": true, "await": true, "break": true, "class": true,
	"continue": true, "def": true, "del": true, "elif": true, "else": true,
	"except": true, "finally": true, "for": true, "from": true, "global": true,
	"if": true, "import": true, "in": true, "is": true, "lambda": true,
	"nonlocal": true, "not": true, "or": true, "pass": true, "raise": true,
	"return": true, "try": true, "while": true, "with": true, "yield": true,
	"match": true, "case": true, "type": true,
	// Shadowing these inside a dataclass is legal but confusing, and the
	// readers use them.
	"int": true, "float": true, "str": true, "bool": true, "list": true,
	"dict": true, "field": true,
}

// pyString renders a Python string literal.
func pyString(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\t", "\\t", "\r", "\\r")
	return "\"" + r.Replace(s) + "\""
}

// pyDocText makes text safe inside a triple-quoted docstring.
func pyDocText(s string) string {
	s = strings.ReplaceAll(s, `"""`, `\"\"\"`)
	return strings.TrimSuffix(s, `\`)
}

// pythonRuntime is the ElementTree helper set the readers are built on.
const pythonRuntime = `def _q(ns, name):
    """The qualified tag ElementTree matches on: {namespace}name."""
    return name if ns is None else "{%s}%s" % (ns, name)


def _local(tag):
    """The local part of a tag, with any {namespace} stripped."""
    return tag.rsplit("}", 1)[-1]


def _children(el, ns, name):
    """Direct children with this name, not descendants at any depth."""
    return [c for c in el if c.tag == _q(ns, name)]


def _child(el, ns, name):
    for c in el:
        if c.tag == _q(ns, name):
            return c
    return None


def _text(el):
    return (el.text or "").strip()


def _child_text(el, ns, name):
    c = _child(el, ns, name)
    return None if c is None else _text(c)


def _attr(el, ns, name):
    # An unqualified attribute is not in the element's namespace, which is why
    # the namespace is passed explicitly rather than inherited.
    return el.get(_q(ns, name))


def _require(value, name):
    if value is None:
        raise ValueError("the schema requires a <%s> element, and it is missing" % name)
    return value


def _require_text(el, ns, name):
    return _text(_require(_child(el, ns, name), name))


def _require_attr(el, ns, name):
    v = _attr(el, ns, name)
    if v is None:
        raise ValueError("the schema requires the %s attribute, and it is missing" % name)
    return v


def _opt(value, fn):
    return None if value is None else fn(value)


def _bool(v):
    return v in ("true", "1")


def _split(v):
    """An xs:list is one element or attribute holding many values."""
    return (v or "").split()
`
