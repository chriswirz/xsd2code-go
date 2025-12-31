package gen

import (
	"fmt"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// This file emits the second C# format: the same classes, but reading and
// writing themselves through IXmlSerializable instead of being described to
// XmlSerializer by attribute.
//
// The point is what the consuming application does at run time. XmlSerializer
// reflects over a type the first time it sees it and emits an assembly to do
// the work, which costs a noticeable pause on the first document and rules the
// generated code out of an application published ahead of time with trimming
// or NativeAOT. The members generated here name every member directly, so the
// whole path is ordinary compiled code.
//
// The shape of a class is unchanged between the two formats, deliberately:
// switching format is a regeneration, not a rewrite of the calling code.

// xsiNamespace is where xsi:nil and xsi:type live.
const xsiNamespace = "http://www.w3.org/2001/XMLSchema-instance"

// genCSharpXMLMembers writes the serialization members of one class: the
// interface implementation on the root of a hierarchy, and the virtuals that
// each derived type extends.
func genCSharpXMLMembers(b *buf, m *ir.Model, t *ir.Type) {
	root := t.Base == ""

	if root {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Always null: the schema is not carried in the assembly, and IXmlSerializable documents this member as reserved.")
		b.doc("/// ", "</summary>")
		b.L("public XmlSchema GetSchema()")
		b.L("{")
		b.In()
		b.L("return null;")
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Reads this element, its attributes and its content, leaving the reader on the node after the element's end tag.")
		b.doc("/// ", "</summary>")
		b.L("public void ReadXml(XmlReader reader)")
		b.L("{")
		b.In()
		b.L("ReadXmlAttributes(reader);")
		b.L("if (reader.IsEmptyElement)")
		b.L("{")
		b.In()
		b.L("reader.Read();")
		b.L("return;")
		b.Out()
		b.L("}")
		b.L("reader.ReadStartElement();")
		b.L("while (!reader.EOF && reader.NodeType != XmlNodeType.EndElement)")
		b.L("{")
		b.In()
		b.L("switch (reader.NodeType)")
		b.L("{")
		b.In()
		b.L("case XmlNodeType.Element:")
		b.In()
		// A child the schema does not declare is skipped rather than
		// rejected, so a document carrying a newer version's content still
		// reads.
		b.L("if (!ReadXmlElement(reader))")
		b.L("{")
		b.In()
		b.L("reader.Skip();")
		b.Out()
		b.L("}")
		b.L("break;")
		b.Out()
		b.L("case XmlNodeType.Text:")
		b.L("case XmlNodeType.CDATA:")
		b.L("case XmlNodeType.SignificantWhitespace:")
		b.In()
		b.L("ReadXmlText(reader.Value);")
		b.L("reader.Read();")
		b.L("break;")
		b.Out()
		b.L("default:")
		b.In()
		b.L("reader.Read();")
		b.L("break;")
		b.Out()
		b.Out()
		b.L("}")
		b.Out()
		b.L("}")
		b.L("if (reader.NodeType == XmlNodeType.EndElement)")
		b.L("{")
		b.In()
		b.L("reader.ReadEndElement();")
		b.Out()
		b.L("}")
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Writes this element's attributes and content. The start and end tags belong to the caller, which is what lets one type be the content of differently named elements.")
		b.doc("/// ", "</summary>")
		b.L("public void WriteXml(XmlWriter writer)")
		b.L("{")
		b.In()
		b.L("WriteXmlAttributes(writer);")
		b.L("WriteXmlElements(writer);")
		b.Out()
		b.L("}")
	}

	if t.Base != "" || len(csharpDescendants(m, t.Name)) > 0 {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "The schema type name written as xsi:type when this instance stands in for a base type.")
		b.doc("/// ", "</summary>")
		b.L("protected internal %s string XsdTypeName", csharpXMLModifier(t, root))
		b.L("{")
		b.In()
		b.L("get { return %q; }", csharpXMLTypeName(t))
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "The namespace of <see cref=\"XsdTypeName\" />.")
		b.doc("/// ", "</summary>")
		b.L("protected internal %s string XsdTypeNamespace", csharpXMLModifier(t, root))
		b.L("{")
		b.In()
		b.L("get { return %q; }", t.Namespace)
		b.Out()
		b.L("}")
	}

	attrs, elems, text := csharpXMLFields(t)

	if root || len(attrs) > 0 {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Reads the attributes of this element. The reader stays on the element itself.")
		b.doc("/// ", "</summary>")
		b.L("protected %s void ReadXmlAttributes(XmlReader reader)", csharpXMLModifier(t, root))
		b.L("{")
		b.In()
		if !root {
			b.L("base.ReadXmlAttributes(reader);")
		}
		for _, f := range attrs {
			genCSharpXMLReadAttribute(b, m, t, f)
		}
		b.Out()
		b.L("}")
	}

	if root || len(elems) > 0 {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Reads one child element, returning false when it is not a member of this type so that the caller can skip it. Matching is on local name: a document that qualifies its content and one that does not both read.")
		b.doc("/// ", "</summary>")
		b.L("protected %s bool ReadXmlElement(XmlReader reader)", csharpXMLModifier(t, root))
		b.L("{")
		b.In()
		var wildcard *ir.Field
		labels := map[string]bool{}
		open := false
		for _, f := range elems {
			if f.Origin == ir.AnyField {
				wildcard = f
				continue
			}
			if labels[f.XMLName] {
				// Two members cannot share a case label. The first one wins,
				// which is the same choice the model's field order makes
				// everywhere else.
				continue
			}
			labels[f.XMLName] = true
			if !open {
				b.L("switch (reader.LocalName)")
				b.L("{")
				b.In()
				open = true
			}
			b.L("case %q:", f.XMLName)
			b.L("{")
			b.In()
			genCSharpXMLReadElement(b, m, f)
			b.L("return true;")
			b.Out()
			b.L("}")
		}
		if open {
			b.Out()
			b.L("}")
		}
		if wildcard != nil {
			b.doc("// ", "Anything else is wildcard content, kept as it arrived.")
			b.L("var any = XsdXml.ReadElement(reader);")
			b.L("if (any != null)")
			b.L("{")
			b.In()
			b.L("%s = XsdXml.Append(%s, any);", wildcard.Name, wildcard.Name)
			b.L("return true;")
			b.Out()
			b.L("}")
		}
		if root {
			b.L("return false;")
		} else {
			b.L("return base.ReadXmlElement(reader);")
		}
		b.Out()
		b.L("}")
	}

	if root || text != nil {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Receives the element's character data, once per run of text.")
		b.doc("/// ", "</summary>")
		b.L("protected %s void ReadXmlText(string text)", csharpXMLModifier(t, root))
		b.L("{")
		b.In()
		if text != nil {
			b.L("%s = %s;", text.Name, csharpXMLParse(m, text, "text"))
		} else if !root {
			b.L("base.ReadXmlText(text);")
		}
		b.Out()
		b.L("}")
	}

	if root || len(attrs) > 0 {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Writes this element's attributes. It must run before any content is written, which is what WriteXml above guarantees.")
		b.doc("/// ", "</summary>")
		b.L("protected %s void WriteXmlAttributes(XmlWriter writer)", csharpXMLModifier(t, root))
		b.L("{")
		b.In()
		if !root {
			b.L("base.WriteXmlAttributes(writer);")
		}
		for _, f := range attrs {
			genCSharpXMLWriteAttribute(b, m, f)
		}
		b.Out()
		b.L("}")
	}

	if root || len(elems) > 0 || text != nil {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Writes this element's content, inherited members first, so that the order matches the sequence the schema declares.")
		b.doc("/// ", "</summary>")
		b.L("protected %s void WriteXmlElements(XmlWriter writer)", csharpXMLModifier(t, root))
		b.L("{")
		b.In()
		if !root {
			b.L("base.WriteXmlElements(writer);")
		}
		if text != nil {
			guard, value := csharpXMLGuard(m, text)
			if guard != "" {
				b.L("if (%s)", guard)
				b.L("{")
				b.In()
			}
			b.L("writer.WriteString(%s);", csharpXMLFormat(m, text, value))
			if guard != "" {
				b.Out()
				b.L("}")
			}
		}
		for _, f := range elems {
			genCSharpXMLWriteElement(b, m, f)
		}
		b.Out()
		b.L("}")
	}
}

// csharpXMLFields splits a type's own members into the three groups the
// generated members handle separately.
func csharpXMLFields(t *ir.Type) (attrs, elems []*ir.Field, text *ir.Field) {
	for _, f := range t.Fields {
		switch f.Origin {
		case ir.AttributeField, ir.AnyAttrField:
			attrs = append(attrs, f)
		case ir.TextField:
			text = f
		default:
			elems = append(elems, f)
		}
	}
	return attrs, elems, text
}

// csharpXMLModifier is "virtual" at the top of a hierarchy and "override"
// below it.
func csharpXMLModifier(t *ir.Type, root bool) string {
	if root {
		return "virtual"
	}
	return "override"
}

// csharpXMLTypeName is the name an instance writes as xsi:type. A type with no
// schema name of its own was synthesized from an anonymous definition, and its
// generated name is the only name there is.
func csharpXMLTypeName(t *ir.Type) string {
	if t.XMLName != "" {
		return t.XMLName
	}
	return t.Name
}

func genCSharpXMLReadAttribute(b *buf, m *ir.Model, t *ir.Type, f *ir.Field) {
	if f.Origin == ir.AnyAttrField {
		b.L("%s = XsdXml.ReadAnyAttributes(reader%s);", f.Name, csharpXMLKnownAttributes(m, t))
		return
	}
	b.L("{")
	b.In()
	b.L("var value = reader.GetAttribute(%q%s);", f.XMLName, csharpXMLNSArg(f.Namespace))
	b.L("if (value != null)")
	b.L("{")
	b.In()
	if f.List {
		b.L("%s.Clear();", f.Name)
		b.L("foreach (var item in XsdXml.SplitList(value))")
		b.L("{")
		b.In()
		b.L("%s.Add(%s);", f.Name, csharpXMLParse(m, f, "item"))
		b.Out()
		b.L("}")
	} else {
		b.L("%s = %s;", f.Name, csharpXMLParse(m, f, "value"))
		if usesSpecified(m, f) {
			b.L("%sSpecified = true;", f.Name)
		}
	}
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
}

// csharpXMLKnownAttributes lists the attribute names the type reads itself,
// the inherited ones included, so that the wildcard collects only what is left
// over.
func csharpXMLKnownAttributes(m *ir.Model, t *ir.Type) string {
	var names []string
	for cur := t; cur != nil; cur = m.Lookup(cur.Base) {
		for _, f := range cur.Fields {
			if f.Origin == ir.AttributeField {
				names = append(names, fmt.Sprintf("%q", f.XMLName))
			}
		}
		if cur.Base == "" {
			break
		}
	}
	out := ""
	for _, n := range names {
		out += ", " + n
	}
	return out
}

func genCSharpXMLReadElement(b *buf, m *ir.Model, f *ir.Field) {
	switch {
	case f.List:
		b.L("var text = reader.ReadElementContentAsString();")
		b.L("%s.Clear();", f.Name)
		b.L("foreach (var item in XsdXml.SplitList(text))")
		b.L("{")
		b.In()
		b.L("%s.Add(%s);", f.Name, csharpXMLParse(m, f, "item"))
		b.Out()
		b.L("}")

	case csharpXMLIsComplex(m, f):
		if f.Nillable {
			b.L("if (XsdXml.IsNil(reader))")
			b.L("{")
			b.In()
			b.L("reader.Skip();")
			if !f.Repeated {
				b.L("%s = null;", f.Name)
			}
			b.L("return true;")
			b.Out()
			b.L("}")
		}
		b.L("var item = %s;", csharpXMLNew(m, f.TypeName, "reader"))
		b.L("item.ReadXml(reader);")
		if f.Repeated {
			b.L("%s.Add(item);", f.Name)
		} else {
			b.L("%s = item;", f.Name)
		}

	default:
		if f.Nillable {
			b.L("if (XsdXml.IsNil(reader))")
			b.L("{")
			b.In()
			b.L("reader.Skip();")
			if !f.Repeated && csharpXMLNullable(m, f) {
				b.L("%s = null;", f.Name)
			}
			b.L("return true;")
			b.Out()
			b.L("}")
		}
		b.L("var text = reader.ReadElementContentAsString();")
		if f.Repeated {
			b.L("%s.Add(%s);", f.Name, csharpXMLParse(m, f, "text"))
		} else {
			b.L("%s = %s;", f.Name, csharpXMLParse(m, f, "text"))
			if usesSpecified(m, f) {
				b.L("%sSpecified = true;", f.Name)
			}
		}
	}
}

func genCSharpXMLWriteAttribute(b *buf, m *ir.Model, f *ir.Field) {
	if f.Origin == ir.AnyAttrField {
		b.L("XsdXml.WriteAnyAttributes(writer, %s);", f.Name)
		return
	}
	if f.List {
		b.L("if (%s != null && %s.Count > 0)", f.Name, f.Name)
		b.L("{")
		b.In()
		genCSharpXMLJoin(b, m, f)
		b.L("writer.WriteAttributeString(%q, %q, string.Join(\" \", parts));", f.XMLName, f.Namespace)
		b.Out()
		b.L("}")
		return
	}
	guard, value := csharpXMLGuard(m, f)
	if guard != "" {
		b.L("if (%s)", guard)
		b.L("{")
		b.In()
	}
	b.L("writer.WriteAttributeString(%q, %q, %s);", f.XMLName, f.Namespace,
		csharpXMLFormat(m, f, value))
	if guard != "" {
		b.Out()
		b.L("}")
	}
}

func genCSharpXMLWriteElement(b *buf, m *ir.Model, f *ir.Field) {
	if f.Origin == ir.AnyField {
		b.L("XsdXml.WriteAnyElements(writer, %s);", f.Name)
		return
	}
	if f.List {
		b.L("if (%s != null && %s.Count > 0)", f.Name, f.Name)
		b.L("{")
		b.In()
		genCSharpXMLJoin(b, m, f)
		b.L("writer.WriteElementString(%q, %q, string.Join(\" \", parts));", f.XMLName, f.Namespace)
		b.Out()
		b.L("}")
		return
	}

	complexField := csharpXMLIsComplex(m, f)
	polymorphic := complexField && len(csharpDescendants(m, f.TypeName)) > 0

	if f.Repeated {
		b.L("if (%s != null)", f.Name)
		b.L("{")
		b.In()
		b.L("foreach (var item in %s)", f.Name)
		b.L("{")
		b.In()
		if complexField {
			b.L("if (item == null)")
			b.L("{")
			b.In()
			b.L("continue;")
			b.Out()
			b.L("}")
			b.L("writer.WriteStartElement(%q, %q);", f.XMLName, f.Namespace)
			if polymorphic {
				b.L("XsdXml.WriteTypeName(writer, item.XsdTypeName, item.XsdTypeNamespace, %q);", csharpXMLDeclaredName(m, f))
			}
			b.L("item.WriteXml(writer);")
			b.L("writer.WriteEndElement();")
		} else {
			b.L("writer.WriteElementString(%q, %q, %s);", f.XMLName, f.Namespace,
				csharpXMLFormat(m, f, "item"))
		}
		b.Out()
		b.L("}")
		b.Out()
		b.L("}")
		return
	}

	guard, value := csharpXMLGuard(m, f)
	if guard != "" {
		b.L("if (%s)", guard)
		b.L("{")
		b.In()
	}
	if complexField {
		b.L("writer.WriteStartElement(%q, %q);", f.XMLName, f.Namespace)
		if polymorphic {
			b.L("XsdXml.WriteTypeName(writer, %s.XsdTypeName, %s.XsdTypeNamespace, %q);",
				value, value, csharpXMLDeclaredName(m, f))
		}
		b.L("%s.WriteXml(writer);", value)
		b.L("writer.WriteEndElement();")
	} else {
		b.L("writer.WriteElementString(%q, %q, %s);", f.XMLName, f.Namespace,
			csharpXMLFormat(m, f, value))
	}
	if guard != "" {
		b.Out()
		b.L("}")
		if f.Nillable {
			// A nillable element that is absent is written as xsi:nil rather
			// than omitted, which is what declaring it nillable asked for.
			b.L("else")
			b.L("{")
			b.In()
			b.L("XsdXml.WriteNil(writer, %q, %q);", f.XMLName, f.Namespace)
			b.Out()
			b.L("}")
		}
	}
}

// csharpXMLDeclaredName is the schema name of a field's declared type, so that
// xsi:type is written only when the instance is actually something else.
func csharpXMLDeclaredName(m *ir.Model, f *ir.Field) string {
	if t := m.Lookup(f.TypeName); t != nil {
		return csharpXMLTypeName(t)
	}
	return ""
}

// genCSharpXMLJoin renders an xs:list member into a local named parts.
func genCSharpXMLJoin(b *buf, m *ir.Model, f *ir.Field) {
	b.L("var parts = new List<string>(%s.Count);", f.Name)
	b.L("foreach (var item in %s)", f.Name)
	b.L("{")
	b.In()
	b.L("parts.Add(%s);", csharpXMLFormat(m, f, "item"))
	b.Out()
	b.L("}")
}

// csharpXMLGuard returns the condition under which a member is written, and
// the expression that yields its value. An empty guard means the member is a
// non-nullable value that is always written.
func csharpXMLGuard(m *ir.Model, f *ir.Field) (guard, value string) {
	switch {
	case usesSpecified(m, f):
		return f.Name + "Specified", f.Name
	case f.Optional && csharpIsValueType(m, f):
		return f.Name + ".HasValue", f.Name + ".Value"
	case csharpXMLIsComplex(m, f) || !csharpIsValueType(m, f):
		// Reference types -- strings, byte arrays, nested classes -- are
		// omitted when null rather than written as an empty element.
		return f.Name + " != null", f.Name
	}
	return "", f.Name
}

// csharpXMLNullable reports whether a member can hold null, which decides
// whether xsi:nil can be represented at all.
func csharpXMLNullable(m *ir.Model, f *ir.Field) bool {
	if csharpXMLIsComplex(m, f) || !csharpIsValueType(m, f) {
		return true
	}
	return f.Optional && !usesSpecified(m, f)
}

// csharpXMLIsComplex reports whether a field holds a generated class.
func csharpXMLIsComplex(m *ir.Model, f *ir.Field) bool {
	if f.TypeName == "" {
		return false
	}
	t := m.Lookup(f.TypeName)
	return t != nil && t.Kind == ir.Class
}

// csharpXMLNew builds an instance of a type to read into: directly when the
// type has no descendants, and through the generated factory when it does,
// since the document's xsi:type decides which class is wanted.
func csharpXMLNew(m *ir.Model, typeName, reader string) string {
	if len(csharpDescendants(m, typeName)) > 0 {
		return fmt.Sprintf("%s.Create(%s)", csharpXMLFactoryName(typeName), reader)
	}
	return "new " + typeName + "()"
}

func csharpXMLFactoryName(typeName string) string { return typeName + "Xml" }

// genCSharpXMLFactory writes the factory that turns an xsi:type into an
// instance. Without it a base-typed member would always read as the base
// class, and a derived instance's own content would be dropped.
func genCSharpXMLFactory(b *buf, m *ir.Model, t *ir.Type) {
	b.doc("/// ", "<summary>")
	b.doc("/// ", fmt.Sprintf("Creates the %s the document asks for, reading xsi:type. A document that names no type gets %s.",
		t.Name, csharpXMLFallbackDoc(t)))
	b.doc("/// ", "</summary>")
	b.L("public static class %s", csharpXMLFactoryName(t.Name))
	b.L("{")
	b.In()
	b.L("public static %s Create(XmlReader reader)", t.Name)
	b.L("{")
	b.In()
	b.L("switch (XsdXml.ReadTypeName(reader))")
	b.L("{")
	b.In()
	for _, name := range csharpDescendants(m, t.Name) {
		sub := m.Lookup(name)
		if sub == nil || sub.Abstract {
			continue
		}
		b.L("case %q:", csharpXMLTypeName(sub))
		b.In()
		b.L("return new %s();", sub.Name)
		b.Out()
	}
	b.Out()
	b.L("}")
	if t.Abstract {
		b.L("throw new XmlException(\"an element of the abstract type %s needs an xsi:type naming one of its derivations\");",
			csharpXMLTypeName(t))
	} else {
		b.L("return new %s();", t.Name)
	}
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
}

func csharpXMLFallbackDoc(t *ir.Type) string {
	if t.Abstract {
		return "an XmlException, since the type is abstract"
	}
	return "the base type itself"
}

// csharpDescendants lists every type that extends the named one, directly or
// through another, in the model's order.
func csharpDescendants(m *ir.Model, name string) []string {
	if name == "" {
		return nil
	}
	found := map[string]bool{name: true}
	var out []string
	// One pass per level is enough: a base always precedes nothing in
	// particular in the model's order, so the loop repeats until it settles.
	for changed := true; changed; {
		changed = false
		for _, t := range m.Types {
			if t.Base != "" && found[t.Base] && !found[t.Name] {
				found[t.Name] = true
				out = append(out, t.Name)
				changed = true
			}
		}
	}
	return out
}

// genCSharpXMLEnumHelper writes the converter for one enumeration. It is a
// switch over the lexical values rather than a lookup through XmlEnumAttribute,
// which is the one place the attribute-based format cannot avoid reflection
// even for a value as simple as this.
func genCSharpXMLEnumHelper(b *buf, t *ir.Type) {
	b.doc("/// ", "<summary>")
	b.doc("/// ", fmt.Sprintf("Converts %s to and from its XML lexical form.", t.Name))
	b.doc("/// ", "</summary>")
	b.L("public static class %s", csharpXMLFactoryName(t.Name))
	b.L("{")
	b.In()
	b.doc("/// ", "<summary>Reads a value, rejecting anything the enumeration does not declare.</summary>")
	b.L("public static %s Parse(string text)", t.Name)
	b.L("{")
	b.In()
	b.L("switch (text)")
	b.L("{")
	b.In()
	seen := map[string]bool{}
	for _, v := range t.Values {
		if seen[v.Value] {
			continue
		}
		seen[v.Value] = true
		b.L("case %q:", v.Value)
		b.In()
		b.L("return %s.%s;", t.Name, v.Name)
		b.Out()
	}
	b.Out()
	b.L("}")
	b.L("throw new FormatException(text + \" is not a value of %s\");", t.Name)
	b.Out()
	b.L("}")
	b.L("")
	b.doc("/// ", "<summary>Writes a value in the form the schema declares.</summary>")
	b.L("public static string Format(%s value)", t.Name)
	b.L("{")
	b.In()
	b.L("switch (value)")
	b.L("{")
	b.In()
	for _, v := range t.Values {
		b.L("case %s.%s:", t.Name, v.Name)
		b.In()
		b.L("return %q;", v.Value)
		b.Out()
	}
	b.Out()
	b.L("}")
	b.L("throw new ArgumentOutOfRangeException(nameof(value));")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
}

// csharpXMLParse renders the expression that turns XML text into the member's
// type.
func csharpXMLParse(m *ir.Model, f *ir.Field, src string) string {
	if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum {
		return fmt.Sprintf("%s.Parse(%s)", csharpXMLFactoryName(t.Name), src)
	}
	switch f.Builtin {
	case ir.Bool:
		return "XmlConvert.ToBoolean(" + src + ")"
	case ir.Byte:
		return "XmlConvert.ToSByte(" + src + ")"
	case ir.Short:
		return "XmlConvert.ToInt16(" + src + ")"
	case ir.Int:
		return "XmlConvert.ToInt32(" + src + ")"
	case ir.Long:
		return "XmlConvert.ToInt64(" + src + ")"
	case ir.UnsignedByte:
		return "XmlConvert.ToByte(" + src + ")"
	case ir.UnsignedShort:
		return "XmlConvert.ToUInt16(" + src + ")"
	case ir.UnsignedInt:
		return "XmlConvert.ToUInt32(" + src + ")"
	case ir.UnsignedLong:
		return "XmlConvert.ToUInt64(" + src + ")"
	case ir.Float:
		return "XmlConvert.ToSingle(" + src + ")"
	case ir.Double:
		return "XmlConvert.ToDouble(" + src + ")"
	case ir.Decimal:
		return "XmlConvert.ToDecimal(" + src + ")"
	case ir.DateTime, ir.Date:
		return "XmlConvert.ToDateTime(" + src + ", XmlDateTimeSerializationMode.RoundtripKind)"
	case ir.Time:
		// XmlConvert.ToTimeSpan reads xs:duration, not xs:time; the lexical
		// form here is 13:45:00, which TimeSpan itself parses.
		return "TimeSpan.Parse(" + src + ", CultureInfo.InvariantCulture)"
	case ir.Base64Binary:
		return "Convert.FromBase64String(" + src + ")"
	case ir.HexBinary:
		return "XsdXml.ParseHexBinary(" + src + ")"
	}
	return src
}

// csharpXMLFormat renders the expression that turns a member's value into XML
// text.
func csharpXMLFormat(m *ir.Model, f *ir.Field, value string) string {
	if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum {
		return fmt.Sprintf("%s.Format(%s)", csharpXMLFactoryName(t.Name), value)
	}
	switch f.Builtin {
	case ir.String, ir.AnyURI, ir.QName, ir.AnyType, ir.Duration:
		return value
	case ir.Date:
		return value + `.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture)`
	case ir.Time:
		return value + `.ToString(@"hh\:mm\:ss", CultureInfo.InvariantCulture)`
	case ir.DateTime:
		return "XmlConvert.ToString(" + value + ", XmlDateTimeSerializationMode.RoundtripKind)"
	case ir.Base64Binary:
		return "Convert.ToBase64String(" + value + ")"
	case ir.HexBinary:
		return "XsdXml.FormatHexBinary(" + value + ")"
	}
	return "XmlConvert.ToString(" + value + ")"
}

// csharpXMLNSArg supplies the namespace argument of GetAttribute, which has a
// one-argument overload for the unqualified case.
func csharpXMLNSArg(ns string) string {
	if ns == "" {
		return ""
	}
	return fmt.Sprintf(", %q", ns)
}

// csharpXMLSupport is the runtime the generated members lean on. Everything in
// it is ordinary code over XmlReader and XmlWriter: no reflection, and nothing
// that a trimmed or ahead-of-time-compiled application has to be told to keep.
const csharpXMLSupport = `    /// <summary>
    /// The pieces of XML handling the generated readers and writers share:
    /// xsi:nil and xsi:type, wildcard content, xs:list splitting and
    /// xs:hexBinary.
    /// </summary>
    public static class XsdXml
    {
        /// <summary>The XML Schema instance namespace, which carries nil and type.</summary>
        public const string InstanceNamespace = "` + xsiNamespace + `";

        /// <summary>
        /// Declares the xsi prefix on the document element, so that a nil or a
        /// type written further down reads as xsi:nil and xsi:type rather than
        /// through a prefix the writer invents where it first needs one.
        /// </summary>
        public static void DeclareInstanceNamespace(XmlWriter writer)
        {
            if (string.IsNullOrEmpty(writer.LookupPrefix(InstanceNamespace)))
            {
                writer.WriteAttributeString("xmlns", "xsi", "http://www.w3.org/2000/xmlns/", InstanceNamespace);
            }
        }

        /// <summary>Reports whether the element the reader is on is marked xsi:nil.</summary>
        public static bool IsNil(XmlReader reader)
        {
            var value = reader.GetAttribute("nil", InstanceNamespace);
            return value == "true" || value == "1";
        }

        /// <summary>Writes an element that carries xsi:nil rather than a value.</summary>
        public static void WriteNil(XmlWriter writer, string name, string ns)
        {
            writer.WriteStartElement(name, ns);
            writer.WriteAttributeString("nil", InstanceNamespace, "true");
            writer.WriteEndElement();
        }

        /// <summary>
        /// Returns the local name of the element's xsi:type, or null when it
        /// declares none. The prefix is dropped: the type is looked up by name
        /// among the derivations the schema declared, and a document is free to
        /// spell the prefix however it likes.
        /// </summary>
        public static string ReadTypeName(XmlReader reader)
        {
            var value = reader.GetAttribute("type", InstanceNamespace);
            if (value == null)
            {
                return null;
            }
            var colon = value.IndexOf(':');
            return colon < 0 ? value : value.Substring(colon + 1);
        }

        /// <summary>
        /// Writes xsi:type, unless the instance is already the type the schema
        /// declares at this position. A prefix for the type's namespace is
        /// declared when the document does not have one.
        /// </summary>
        public static void WriteTypeName(XmlWriter writer, string name, string ns, string declared)
        {
            if (name == null || name == declared)
            {
                return;
            }
            if (string.IsNullOrEmpty(ns))
            {
                writer.WriteAttributeString("type", InstanceNamespace, name);
                return;
            }
            var prefix = writer.LookupPrefix(ns);
            if (prefix == null)
            {
                prefix = "xsd2code";
                writer.WriteAttributeString("xmlns", prefix, "http://www.w3.org/2000/xmlns/", ns);
            }
            else if (prefix.Length == 0)
            {
                // The namespace is the document's default one, and a QName in
                // an attribute value does resolve through it, so the bare name
                // is right and spares the content an invented prefix.
                writer.WriteAttributeString("type", InstanceNamespace, name);
                return;
            }
            writer.WriteAttributeString("type", InstanceNamespace, prefix + ":" + name);
        }

        /// <summary>Splits an xs:list value on whitespace.</summary>
        public static string[] SplitList(string value)
        {
            if (string.IsNullOrWhiteSpace(value))
            {
                return new string[0];
            }
            return value.Split((char[])null, StringSplitOptions.RemoveEmptyEntries);
        }

        /// <summary>Reads the current element, and everything under it, as raw XML.</summary>
        public static XmlElement ReadElement(XmlReader reader)
        {
            var owner = new XmlDocument();
            return owner.ReadNode(reader) as XmlElement;
        }

        /// <summary>Writes wildcard element content back exactly as it was read.</summary>
        public static void WriteAnyElements(XmlWriter writer, XmlElement[] elements)
        {
            if (elements == null)
            {
                return;
            }
            foreach (var element in elements)
            {
                if (element != null)
                {
                    element.WriteTo(writer);
                }
            }
        }

        /// <summary>
        /// Collects the attributes of the current element that the type does not
        /// declare itself. Namespace declarations and the xsi attributes are
        /// left out: they describe the document, not its content. The reader is
        /// returned to the element.
        /// </summary>
        public static XmlAttribute[] ReadAnyAttributes(XmlReader reader, params string[] known)
        {
            if (!reader.HasAttributes)
            {
                return null;
            }
            var owner = new XmlDocument();
            var found = new List<XmlAttribute>();
            if (reader.MoveToFirstAttribute())
            {
                do
                {
                    if (reader.Prefix == "xmlns" || reader.Name == "xmlns")
                    {
                        continue;
                    }
                    if (reader.NamespaceURI == InstanceNamespace)
                    {
                        continue;
                    }
                    if (Array.IndexOf(known, reader.LocalName) >= 0)
                    {
                        continue;
                    }
                    var attribute = owner.CreateAttribute(reader.Prefix, reader.LocalName, reader.NamespaceURI);
                    attribute.Value = reader.Value;
                    found.Add(attribute);
                }
                while (reader.MoveToNextAttribute());
                reader.MoveToElement();
            }
            return found.Count == 0 ? null : found.ToArray();
        }

        /// <summary>Writes wildcard attributes back as they were read.</summary>
        public static void WriteAnyAttributes(XmlWriter writer, XmlAttribute[] attributes)
        {
            if (attributes == null)
            {
                return;
            }
            foreach (var attribute in attributes)
            {
                if (attribute != null)
                {
                    writer.WriteAttributeString(attribute.Prefix, attribute.LocalName, attribute.NamespaceURI, attribute.Value);
                }
            }
        }

        /// <summary>Appends to an array, which is the shape wildcard content has.</summary>
        public static T[] Append<T>(T[] array, T item)
        {
            if (array == null)
            {
                return new[] { item };
            }
            var grown = new T[array.Length + 1];
            Array.Copy(array, grown, array.Length);
            grown[array.Length] = item;
            return grown;
        }

        /// <summary>Reads xs:hexBinary.</summary>
        public static byte[] ParseHexBinary(string value)
        {
            if (value == null)
            {
                return null;
            }
            value = value.Trim();
            if (value.Length % 2 != 0)
            {
                throw new FormatException("a hexBinary value has an even number of digits");
            }
            var bytes = new byte[value.Length / 2];
            for (var i = 0; i < bytes.Length; i++)
            {
                bytes[i] = (byte)((HexDigit(value[i * 2]) << 4) | HexDigit(value[i * 2 + 1]));
            }
            return bytes;
        }

        /// <summary>Writes xs:hexBinary, in the upper case the datatype uses.</summary>
        public static string FormatHexBinary(byte[] value)
        {
            if (value == null)
            {
                return null;
            }
            var text = new char[value.Length * 2];
            for (var i = 0; i < value.Length; i++)
            {
                text[i * 2] = Hex[value[i] >> 4];
                text[i * 2 + 1] = Hex[value[i] & 0xF];
            }
            return new string(text);
        }

        private const string Hex = "0123456789ABCDEF";

        private static int HexDigit(char c)
        {
            if (c >= '0' && c <= '9')
            {
                return c - '0';
            }
            if (c >= 'a' && c <= 'f')
            {
                return c - 'a' + 10;
            }
            if (c >= 'A' && c <= 'F')
            {
                return c - 'A' + 10;
            }
            throw new FormatException(c + " is not a hexadecimal digit");
        }
    }`
