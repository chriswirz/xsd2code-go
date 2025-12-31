package gen

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genKotlin emits data classes and the readers that fill them from the DOM the
// JDK already ships. JAXB would be the obvious alternative, but it wants
// mutable properties and a no-argument constructor, which is the opposite of
// what a Kotlin data class is; this way the generated file has no dependency
// beyond the standard library.
func genKotlin(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	pkg := o.Package
	if pkg == "" {
		pkg = "generated.models"
	}
	dir := strings.ReplaceAll(pkg, ".", "/") + "/"

	b := newBuf("    ")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	b.L("package %s", pkg)
	b.L("")
	b.L("import java.io.File")
	b.L("import javax.xml.parsers.DocumentBuilderFactory")
	// Aliased, because a schema is free to declare a type called Node, Element
	// or Document, and in Kotlin an explicit import wins over a declaration in
	// the same package -- so the generated type would lose.
	b.L("import org.w3c.dom.Document as DomDocument")
	b.L("import org.w3c.dom.Element as DomElement")
	b.L("import org.w3c.dom.Node as DomNode")
	b.L("")
	if ns := m.TargetNamespace; ns != "" {
		b.doc("// ", "The namespace every element of this schema lives in.")
		b.L("const val NAMESPACE: String = %q", ns)
	} else {
		b.doc("// ", "This schema is unqualified: its elements have no namespace.")
		b.L("val NAMESPACE: String? = null")
	}
	b.L("")
	b.L("%s", kotlinRuntime)

	for _, t := range m.Types {
		b.L("")
		if t.Kind == ir.Enum {
			genKotlinEnum(b, t)
		} else {
			genKotlinClass(b, m, rel, t)
		}
	}
	for _, t := range m.Types {
		if t.Kind == ir.Enum {
			continue
		}
		b.L("")
		genKotlinReader(b, m, t)
	}
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		b.L("")
		genKotlinRoot(b, root)
	}
	return []File{{Name: dir + "Models.kt", Content: b.Bytes()}}, nil
}

// genKotlinEnum writes an enum whose constants carry their XML lexical value.
func genKotlinEnum(b *buf, t *ir.Type) {
	kotlinDoc(b, firstNonEmpty(t.Doc, fmt.Sprintf("The %q enumeration.", t.XMLName)))
	b.L("enum class %s(val value: String) {", t.Name)
	b.In()
	names := newNames()
	for i, v := range t.Values {
		if v.Doc != "" {
			kotlinDoc(b, v.Doc)
		}
		sep := ","
		if i == len(t.Values)-1 {
			sep = ";"
		}
		b.L("%s(%q)%s", names.take(pyEnumName(v)), v.Value, sep)
	}
	if len(t.Values) == 0 {
		b.L(";")
	}
	b.L("")
	b.L("companion object {")
	b.In()
	kotlinDoc(b, "Returns the constant with this XML lexical value.")
	b.L("fun fromValue(value: String): %s =", t.Name)
	b.In()
	b.L("entries.firstOrNull { it.value == value }")
	b.L("?: throw IllegalArgumentException(\"$value is not a value of %s\")", t.Name)
	b.Out()
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
}

// genKotlinClass writes one data class.
func genKotlinClass(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type) {
	kotlinDoc(b, firstNonEmpty(t.Doc, fmt.Sprintf("The %q complex type.", t.XMLName)))
	if t.Base != "" {
		// A data class cannot extend a data class, and giving up either the
		// generated equals/copy or the flat shape would cost more than the
		// duplication does.
		kotlinDoc(b, fmt.Sprintf("Extends %s in the schema; its members are declared here, because a Kotlin data class cannot inherit from one.", t.Base))
	}

	fields := flatFields(m, t)
	if len(fields) == 0 {
		// A class with no members cannot be a data class.
		b.L("class %s", t.Name)
		return
	}
	b.L("data class %s(", t.Name)
	b.In()
	names := newNames()
	for i, f := range fields {
		if d := fieldDoc(t, f); d != "" {
			kotlinDoc(b, d)
		}
		comma := ","
		if i == len(fields)-1 {
			comma = ""
		}
		b.L("val %s: %s = %s%s", kotlinName(names.take(ir.Camel(f.Name))), kotlinType(m, f), kotlinDefault(m, f), comma)
	}
	b.Out()
	if rel == nil {
		b.L(")")
		return
	}
	tbl := rel.Table(t.Name)
	if tbl == nil {
		b.L(")")
		return
	}
	b.L(") {")
	b.In()
	b.L("companion object {")
	b.In()
	kotlinDoc(b, "The table this type is stored in.")
	b.L("const val TABLE = %q", tbl.Name)
	kotlinDoc(b, "Each member's column, for a caller assembling its own SQL.")
	// Annotated, because a type whose every member is repeated complex
	// content has no columns at all, and mapOf() alone cannot be inferred.
	b.L("val COLUMNS: Map<String, String> = mapOf(")
	b.In()
	colNames := newNames()
	for _, f := range fields {
		if col := columnObj(tbl, f); col != nil {
			b.L("%q to %q,", kotlinName(colNames.take(ir.Camel(f.Name))), col.Name)
		}
	}
	b.Out()
	b.L(")")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
}

// genKotlinReader writes the function that builds one object from an element.
func genKotlinReader(b *buf, m *ir.Model, t *ir.Type) {
	kotlinDoc(b, fmt.Sprintf("Reads a %s from the element that contains it.", t.Name))
	fields := flatFields(m, t)
	if len(fields) == 0 {
		b.L("fun read%s(el: DomElement): %s = %s()", t.Name, t.Name, t.Name)
		return
	}
	b.L("fun read%s(el: DomElement): %s = %s(", t.Name, t.Name, t.Name)
	b.In()
	names := newNames()
	for i, f := range fields {
		comma := ","
		if i == len(fields)-1 {
			comma = ""
		}
		b.L("%s = %s%s", kotlinName(names.take(ir.Camel(f.Name))), kotlinRead(m, f), comma)
	}
	b.Out()
	b.L(")")
}

// genKotlinRoot writes the document entry points.
func genKotlinRoot(b *buf, root *ir.Root) {
	name := ir.Pascal(root.XMLName)
	doc := fmt.Sprintf("Parses a %q document.", root.XMLName)
	if root.Doc != "" {
		doc += " " + root.Doc
	}
	kotlinDoc(b, doc)
	b.L("fun parse%s(xml: String): %s = read%s(rootElement(parseDocument(xml), %q))",
		name, root.Type, root.Type, root.XMLName)
	b.L("")
	kotlinDoc(b, fmt.Sprintf("Reads a %q document from a file.", root.XMLName))
	b.L("fun load%s(file: File): %s = read%s(rootElement(parseDocument(file), %q))",
		name, root.Type, root.Type, root.XMLName)
}

// flatFields returns the members a type declares, inherited ones included.
// Kotlin data classes and Swift structs both refuse to inherit, so an
// extension's members are written into each type that extends it.
func flatFields(m *ir.Model, t *ir.Type) []*ir.Field {
	if t.Base == "" {
		return t.Fields
	}
	return append(inheritedFields(m, t.Base), t.Fields...)
}

// kotlinType renders a member's declared type.
func kotlinType(m *ir.Model, f *ir.Field) string {
	switch f.Origin {
	case ir.AnyField:
		return "List<String>"
	case ir.AnyAttrField:
		return "Map<String, String>"
	}
	base := f.TypeName
	if base == "" {
		base = kotlinBuiltin(f.Builtin)
	}
	switch {
	case f.Repeated, f.List:
		return "List<" + base + ">"
	case f.Optional:
		return base + "?"
	}
	return base
}

func kotlinBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "Boolean"
	case ir.Byte:
		return "Byte"
	case ir.Short, ir.UnsignedByte:
		return "Short"
	case ir.Int, ir.UnsignedShort:
		return "Int"
	case ir.Long, ir.UnsignedInt:
		return "Long"
	case ir.UnsignedLong:
		return "java.math.BigInteger"
	case ir.Float:
		return "Float"
	case ir.Double:
		return "Double"
	case ir.Decimal:
		return "java.math.BigDecimal"
	}
	// The temporal and binary types are kept lexical: each would need a
	// conversion the caller may want to make differently, and the string
	// always round-trips.
	return "String"
}

// kotlinDefault gives every member a default, so a caller can build one from
// the parts it has.
func kotlinDefault(m *ir.Model, f *ir.Field) string {
	switch {
	case f.Origin == ir.AnyAttrField:
		return "emptyMap()"
	case f.Repeated, f.List, f.Origin == ir.AnyField:
		return "emptyList()"
	case f.Optional:
		return "null"
	}
	switch kotlinType(m, f) {
	case "String":
		return `""`
	case "Boolean":
		return "false"
	case "Int":
		return "0"
	case "Long":
		return "0L"
	case "Short":
		return "0"
	case "Byte":
		return "0"
	case "Float":
		return "0.0f"
	case "Double":
		return "0.0"
	case "java.math.BigDecimal":
		return "java.math.BigDecimal.ZERO"
	case "java.math.BigInteger":
		return "java.math.BigInteger.ZERO"
	}
	// A required complex member has no sensible empty value, so it stays
	// required at the call site.
	return kotlinType(m, f) + "()"
}

// kotlinRead renders the expression that decodes one member.
func kotlinRead(m *ir.Model, f *ir.Field) string {
	ns := "NAMESPACE"
	if f.Namespace == "" {
		ns = "null"
	}
	conv := kotlinConvert(m, f)

	switch f.Origin {
	case ir.AnyAttrField:
		return "anyAttributes(el)"
	case ir.AnyField:
		return "childElements(el, null, null).map { nodeText(it) }"
	case ir.TextField:
		return kotlinApply(conv, "textOf(el)")
	case ir.AttributeField:
		source := fmt.Sprintf("attrOf(el, %s, %q)", ns, f.XMLName)
		if f.List {
			return kotlinMapList(fmt.Sprintf("splitList(%s)", source), conv)
		}
		if f.Optional {
			if conv == "" {
				return source
			}
			return fmt.Sprintf("%s?.let { %s }", source, kotlinApply(conv, "it"))
		}
		return kotlinApply(conv, fmt.Sprintf("requireAttr(el, %s, %q)", ns, f.XMLName))
	}

	if f.Repeated {
		if f.TypeName != "" && isClass(m, f.TypeName) {
			return fmt.Sprintf("childElements(el, %s, %q).map { read%s(it) }", ns, f.XMLName, f.TypeName)
		}
		return fmt.Sprintf("childElements(el, %s, %q).map { %s }", ns, f.XMLName, kotlinApply(conv, "textOf(it)"))
	}
	if f.List {
		return kotlinMapList(fmt.Sprintf("splitList(childText(el, %s, %q))", ns, f.XMLName), conv)
	}
	if f.TypeName != "" && isClass(m, f.TypeName) {
		child := fmt.Sprintf("childElement(el, %s, %q)", ns, f.XMLName)
		if f.Optional {
			return fmt.Sprintf("%s?.let { read%s(it) }", child, f.TypeName)
		}
		return fmt.Sprintf("read%s(requireChild(el, %s, %q))", f.TypeName, ns, f.XMLName)
	}
	source := fmt.Sprintf("childText(el, %s, %q)", ns, f.XMLName)
	if f.Optional {
		if conv == "" {
			return source
		}
		return fmt.Sprintf("%s?.let { %s }", source, kotlinApply(conv, "it"))
	}
	return kotlinApply(conv, fmt.Sprintf("requireText(el, %s, %q)", ns, f.XMLName))
}

func kotlinApply(conv, source string) string {
	if conv == "" {
		return source
	}
	return fmt.Sprintf(conv, source)
}

// kotlinMapList converts each item of a split list, leaving strings alone.
func kotlinMapList(split, conv string) string {
	if conv == "" {
		return split
	}
	return fmt.Sprintf("%s.map { %s }", split, kotlinApply(conv, "it"))
}

// kotlinConvert returns a format string that wraps a lexical value in the
// conversion its type needs, or "" when the string is already the value.
func kotlinConvert(m *ir.Model, f *ir.Field) string {
	if f.TypeName != "" {
		if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum {
			return f.TypeName + ".fromValue(%s)"
		}
		return ""
	}
	switch f.Builtin {
	case ir.Bool:
		return "toBoolean(%s)"
	case ir.Byte:
		return "%s.toByte()"
	case ir.Short, ir.UnsignedByte:
		return "%s.toShort()"
	case ir.Int, ir.UnsignedShort:
		return "%s.toInt()"
	case ir.Long, ir.UnsignedInt:
		return "%s.toLong()"
	case ir.UnsignedLong:
		return "java.math.BigInteger(%s)"
	case ir.Float:
		return "%s.toFloat()"
	case ir.Double:
		return "%s.toDouble()"
	case ir.Decimal:
		return "java.math.BigDecimal(%s)"
	}
	return ""
}

// kotlinName escapes a member name that is a Kotlin keyword. Backticks make
// this lossless.
func kotlinName(name string) string {
	if name == "" {
		return "value"
	}
	if kotlinKeywords[name] {
		return "`" + name + "`"
	}
	return name
}

var kotlinKeywords = map[string]bool{
	"as": true, "break": true, "class": true, "continue": true, "do": true,
	"else": true, "false": true, "for": true, "fun": true, "if": true,
	"in": true, "interface": true, "is": true, "null": true, "object": true,
	"package": true, "return": true, "super": true, "this": true, "throw": true,
	"true": true, "try": true, "typealias": true, "typeof": true, "val": true,
	"var": true, "when": true, "while": true,
}

// kotlinDoc writes a KDoc comment.
func kotlinDoc(b *buf, text string) {
	lines := wrap(text, 72)
	if len(lines) == 0 {
		return
	}
	if len(lines) == 1 {
		b.L("/** %s */", lines[0])
		return
	}
	b.L("/**")
	for _, line := range lines {
		b.L(" * %s", line)
	}
	b.L(" */")
}

// kotlinRuntime is the DOM helper set the readers are built on, emitted into
// the file so it depends on nothing but the JDK.
const kotlinRuntime = `// The DOM helpers the readers below are built on. They use the parser the JDK
// already ships, so this file needs no dependency at all.

private fun documentBuilder() = DocumentBuilderFactory.newInstance().apply {
    isNamespaceAware = true
    // A generated parser has no business resolving external entities: it is the
    // XXE hole, and no schema needs it.
    setFeature("http://apache.org/xml/features/disallow-doctype-decl", true)
    isXIncludeAware = false
    isExpandEntityReferences = false
}.newDocumentBuilder()

/** Parses a document held in a string. */
fun parseDocument(xml: String): DomDocument =
    documentBuilder().parse(xml.byteInputStream())

/** Parses a document from a file. */
fun parseDocument(file: File): DomDocument = documentBuilder().parse(file)

/** The document element, checked against the name the schema declares. */
fun rootElement(doc: DomDocument, name: String): DomElement {
    val root = doc.documentElement
        ?: throw IllegalArgumentException("the document is empty")
    if (root.localName != name && root.nodeName != name) {
        throw IllegalArgumentException("expected a <$name> document, got <${root.nodeName}>")
    }
    return root
}

// Direct children, not getElementsByTagName, which would reach into nested
// elements of the same name and quietly duplicate content.
internal fun childElements(el: DomElement, ns: String?, name: String?): List<DomElement> {
    val out = ArrayList<DomElement>()
    val children = el.childNodes
    for (i in 0 until children.length) {
        val node = children.item(i)
        if (node.nodeType != DomNode.ELEMENT_NODE) continue
        val child = node as DomElement
        if (name != null && child.localName != name && child.nodeName != name) continue
        if (ns != null && child.namespaceURI != ns) continue
        out.add(child)
    }
    return out
}

internal fun childElement(el: DomElement, ns: String?, name: String): DomElement? =
    childElements(el, ns, name).firstOrNull()

internal fun childText(el: DomElement, ns: String?, name: String): String? =
    childElement(el, ns, name)?.let { textOf(it) }

internal fun textOf(el: DomElement): String = (el.textContent ?: "").trim()

internal fun nodeText(el: DomElement): String = el.textContent ?: ""

internal fun attrOf(el: DomElement, ns: String?, name: String): String? {
    // An unqualified attribute is not in the element's namespace, which is why
    // the namespace is passed explicitly rather than inherited.
    val value = if (ns == null) el.getAttribute(name) else el.getAttributeNS(ns, name)
    return if (value.isNullOrEmpty() && !el.hasAttribute(name)) null else value
}

internal fun anyAttributes(el: DomElement): Map<String, String> {
    val out = LinkedHashMap<String, String>()
    val attrs = el.attributes
    for (i in 0 until attrs.length) {
        val a = attrs.item(i)
        if (a.nodeName == "xmlns" || a.nodeName.startsWith("xmlns:")) continue
        out[a.nodeName] = a.nodeValue
    }
    return out
}

internal fun requireChild(el: DomElement, ns: String?, name: String): DomElement =
    childElement(el, ns, name)
        ?: throw IllegalArgumentException("the schema requires a <$name> element, and it is missing")

internal fun requireText(el: DomElement, ns: String?, name: String): String =
    textOf(requireChild(el, ns, name))

internal fun requireAttr(el: DomElement, ns: String?, name: String): String =
    attrOf(el, ns, name)
        ?: throw IllegalArgumentException("the schema requires the $name attribute, and it is missing")

internal fun toBoolean(value: String): Boolean = value == "true" || value == "1"

/** An xs:list is one element or attribute holding many values. */
internal fun splitList(value: String?): List<String> =
    value?.trim()?.takeIf { it.isNotEmpty() }?.split(Regex("\\s+")) ?: emptyList()`
