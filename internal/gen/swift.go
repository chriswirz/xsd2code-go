package gen

import (
	"fmt"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genSwift emits structs and the readers that fill them.
//
// Foundation has no tree-shaped XML API that exists on every platform, so the
// support code drives XMLParser once to build a small node tree and the readers
// walk that. On Linux the XML half of Foundation is a module of its own, so the
// import is conditional; the result then needs no package dependency on macOS,
// iOS or Linux alike.
func genSwift(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	b := newBuf("    ")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	b.L("import Foundation")
	// On Linux the XML types live in FoundationXML, which does not exist on
	// Apple platforms; canImport is the only spelling that satisfies both.
	b.L("#if canImport(FoundationXML)")
	b.L("import FoundationXML")
	b.L("#endif")
	b.L("")
	if ns := m.TargetNamespace; ns != "" {
		b.doc("// ", "The namespace every element of this schema lives in.")
		b.L("public let namespaceURI: String? = %q", ns)
	} else {
		b.doc("// ", "This schema is unqualified: its elements have no namespace.")
		b.L("public let namespaceURI: String? = nil")
	}
	b.L("")
	b.L("%s", swiftRuntime)

	// A type that can contain itself cannot be a struct: a value type has to
	// have a size the compiler can work out.
	byReference := selfReferential(m)
	for _, t := range m.Types {
		b.L("")
		if t.Kind == ir.Enum {
			genSwiftEnum(b, t)
		} else {
			genSwiftStruct(b, m, rel, t, byReference)
		}
	}
	for _, t := range m.Types {
		if t.Kind == ir.Enum {
			continue
		}
		b.L("")
		genSwiftReader(b, m, t, byReference)
	}
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		b.L("")
		genSwiftRoot(b, root)
	}
	return []File{{Name: "Models.swift", Content: b.Bytes()}}, nil
}

// genSwiftEnum writes a String-backed enum, so a case is its lexical value.
func genSwiftEnum(b *buf, t *ir.Type) {
	swiftDoc(b, "", firstNonEmpty(t.Doc, fmt.Sprintf("The %q enumeration.", t.XMLName)))
	b.L("public enum %s: String, Codable, CaseIterable {", t.Name)
	b.In()
	names := newNames()
	for _, v := range t.Values {
		if v.Doc != "" {
			swiftDoc(b, "", v.Doc)
		}
		b.L("case %s = %q", swiftName(names.take(ir.Camel(ir.Pascal(v.Value)))), v.Value)
	}
	b.Out()
	b.L("}")
}

// genSwiftStruct writes one complex type.
func genSwiftStruct(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type, byReference map[string]bool) {
	swiftDoc(b, "", firstNonEmpty(t.Doc, fmt.Sprintf("The %q complex type.", t.XMLName)))
	if t.Base != "" {
		// A struct cannot inherit, and a class hierarchy would cost the value
		// semantics that make the rest of these types pleasant to use.
		swiftDoc(b, "", fmt.Sprintf("Extends %s in the schema; its members are declared here, because a Swift struct cannot inherit.", t.Base))
	}

	kind := "public struct"
	if byReference[t.Name] {
		swiftDoc(b, "", "A class rather than a struct: this type can contain itself, and a value type has to have a size the compiler can work out.")
		kind = "public final class"
	}
	// No explicit Sendable: Foundation's Decimal does not conform on every
	// platform, and a struct of sendable members gets the conformance on its
	// own wherever it actually holds.
	b.L("%s %s {", kind, t.Name)
	b.In()

	fields := flatFields(m, t)
	names := newNames()
	members := make([]string, 0, len(fields))
	for _, f := range fields {
		name := swiftName(names.take(ir.Camel(f.Name)))
		members = append(members, name)
		if d := fieldDoc(t, f); d != "" {
			swiftDoc(b, "", d)
		}
		b.L("public var %s: %s", name, swiftType(m, f))
	}

	if rel != nil {
		if tbl := rel.Table(t.Name); tbl != nil {
			b.L("")
			swiftDoc(b, "", "The table this type is stored in.")
			b.L("public static let table = %q", tbl.Name)
			swiftDoc(b, "", "Each member's column, for a caller assembling its own SQL.")
			var entries [][2]string
			colNames := newNames()
			for _, f := range fields {
				if col := columnObj(tbl, f); col != nil {
					entries = append(entries, [2]string{swiftName(colNames.take(ir.Camel(f.Name))), col.Name})
				}
			}
			if len(entries) == 0 {
				// A type whose every member is repeated complex content has no
				// columns, and an empty dictionary is spelled [:] in Swift.
				b.L("public static let columns: [String: String] = [:]")
			} else {
				b.L("public static let columns: [String: String] = [")
				b.In()
				for _, e := range entries {
					b.L("%q: %q,", e[0], e[1])
				}
				b.Out()
				b.L("]")
			}
		}
	}

	// A memberwise initializer with defaults. Swift synthesizes one for a
	// struct, but not a public one, and not at all for a class.
	b.L("")
	if len(fields) == 0 {
		b.L("public init() {}")
	} else {
		b.L("public init(")
		b.In()
		for i, f := range fields {
			comma := ","
			if i == len(fields)-1 {
				comma = ""
			}
			b.L("%s: %s = %s%s", members[i], swiftType(m, f), swiftDefault(m, f), comma)
		}
		b.Out()
		b.L(") {")
		b.In()
		for i := range fields {
			b.L("self.%s = %s", members[i], members[i])
		}
		b.Out()
		b.L("}")
	}

	b.Out()
	b.L("}")
}

// genSwiftReader writes the function that builds one value from a node.
func genSwiftReader(b *buf, m *ir.Model, t *ir.Type, byReference map[string]bool) {
	swiftDoc(b, "", fmt.Sprintf("Reads a %s from the element that contains it.", t.Name))
	b.L("public func read%s(_ el: XSDElement) throws -> %s {", t.Name, t.Name)
	b.In()
	fields := flatFields(m, t)
	if len(fields) == 0 {
		b.L("return %s()", t.Name)
		b.Out()
		b.L("}")
		return
	}
	b.L("return %s(", t.Name)
	b.In()
	names := newNames()
	for i, f := range fields {
		comma := ","
		if i == len(fields)-1 {
			comma = ""
		}
		b.L("%s: %s%s", swiftName(names.take(ir.Camel(f.Name))), swiftRead(m, f), comma)
	}
	b.Out()
	b.L(")")
	b.Out()
	b.L("}")
}

// genSwiftRoot writes the document entry points.
func genSwiftRoot(b *buf, root *ir.Root) {
	name := ir.Pascal(root.XMLName)
	doc := fmt.Sprintf("Parses a %q document.", root.XMLName)
	if root.Doc != "" {
		doc += " " + root.Doc
	}
	swiftDoc(b, "", doc)
	b.L("public func parse%s(_ xml: String) throws -> %s {", name, root.Type)
	b.In()
	b.L("let root = try rootElement(parseDocument(Data(xml.utf8)), named: %q)", root.XMLName)
	b.L("return try read%s(root)", root.Type)
	b.Out()
	b.L("}")
	b.L("")
	swiftDoc(b, "", fmt.Sprintf("Reads a %q document from a file.", root.XMLName))
	b.L("public func load%s(contentsOf url: URL) throws -> %s {", name, root.Type)
	b.In()
	b.L("let root = try rootElement(parseDocument(try Data(contentsOf: url)), named: %q)", root.XMLName)
	b.L("return try read%s(root)", root.Type)
	b.Out()
	b.L("}")
}

// swiftType renders a member's declared type.
func swiftType(m *ir.Model, f *ir.Field) string {
	switch f.Origin {
	case ir.AnyField:
		return "[XSDElement]"
	case ir.AnyAttrField:
		return "[String: String]"
	}
	base := f.TypeName
	if base == "" {
		base = swiftBuiltin(f.Builtin)
	}
	switch {
	case f.Repeated, f.List:
		return "[" + base + "]"
	case f.Optional:
		return base + "?"
	}
	return base
}

func swiftBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "Bool"
	case ir.Byte:
		return "Int8"
	case ir.Short:
		return "Int16"
	case ir.Int:
		return "Int32"
	case ir.Long:
		return "Int64"
	case ir.UnsignedByte:
		return "UInt8"
	case ir.UnsignedShort:
		return "UInt16"
	case ir.UnsignedInt:
		return "UInt32"
	case ir.UnsignedLong:
		return "UInt64"
	case ir.Float:
		return "Float"
	case ir.Double:
		return "Double"
	case ir.Decimal:
		return "Decimal"
	}
	return "String"
}

// swiftDefault gives every member of the initializer a default.
func swiftDefault(m *ir.Model, f *ir.Field) string {
	switch {
	case f.Origin == ir.AnyAttrField:
		return "[:]"
	case f.Repeated, f.List, f.Origin == ir.AnyField:
		return "[]"
	case f.Optional:
		return "nil"
	}
	switch typ := swiftType(m, f); typ {
	case "String":
		return `""`
	case "Bool":
		return "false"
	case "Decimal":
		return "0"
	case "Float", "Double":
		return "0"
	case "Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64":
		return "0"
	default:
		if f.TypeName != "" {
			if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum && len(t.Values) > 0 {
				return "." + swiftName(ir.Camel(ir.Pascal(t.Values[0].Value)))
			}
		}
		return typ + "()"
	}
}

// swiftRead renders the expression that decodes one member.
//
// Swift wants `try` exactly where something throws: once at the front of an
// expression, and again inside a closure that itself throws, but nowhere else.
// A stray one is a warning and a doubled one does not compile, so the pieces
// below each report whether they throw rather than assuming it.
func swiftRead(m *ir.Model, f *ir.Field) string {
	ns := "namespaceURI"
	if f.Namespace == "" {
		ns = "nil"
	}
	conv, convThrows := swiftConvert(m, f)

	switch f.Origin {
	case ir.AnyAttrField:
		return "el.attributes"
	case ir.AnyField:
		return "el.children"
	case ir.TextField:
		return swiftValue(conv, convThrows, "el.text", false)
	case ir.AttributeField:
		if f.List {
			return swiftMapped(fmt.Sprintf("splitList(el.attribute(%q, namespace: %s))", f.XMLName, ns), conv, convThrows)
		}
		if f.Optional {
			return swiftMapped(fmt.Sprintf("el.attribute(%q, namespace: %s)", f.XMLName, ns), conv, convThrows)
		}
		return swiftValue(conv, convThrows,
			fmt.Sprintf("el.requireAttribute(%q, namespace: %s)", f.XMLName, ns), true)
	}

	// A child element.
	if f.Repeated {
		source := fmt.Sprintf("el.children(%q, namespace: %s)", f.XMLName, ns)
		if f.TypeName != "" && isClass(m, f.TypeName) {
			return fmt.Sprintf("try %s.map { try read%s($0) }", source, f.TypeName)
		}
		return swiftMapped(source+".map { $0.text }", conv, convThrows)
	}
	if f.List {
		return swiftMapped(fmt.Sprintf("splitList(el.childText(%q, namespace: %s))", f.XMLName, ns), conv, convThrows)
	}
	if f.TypeName != "" && isClass(m, f.TypeName) {
		if f.Optional {
			return fmt.Sprintf("try el.child(%q, namespace: %s).map { try read%s($0) }", f.XMLName, ns, f.TypeName)
		}
		return fmt.Sprintf("try read%s(el.requireChild(%q, namespace: %s))", f.TypeName, f.XMLName, ns)
	}
	if f.Optional {
		return swiftMapped(fmt.Sprintf("el.childText(%q, namespace: %s)", f.XMLName, ns), conv, convThrows)
	}
	return swiftValue(conv, convThrows, fmt.Sprintf("el.requireText(%q, namespace: %s)", f.XMLName, ns), true)
}

// swiftValue renders one value, converted if it needs converting, with a
// single try at the front if anything in it throws.
func swiftValue(conv string, convThrows bool, source string, sourceThrows bool) string {
	expr := source
	if conv != "" {
		expr = fmt.Sprintf(conv, source)
	}
	if convThrows || sourceThrows {
		return "try " + expr
	}
	return expr
}

// swiftMapped converts through map, which is what an optional and a collection
// both want: nothing to do when there is nothing there.
func swiftMapped(source, conv string, convThrows bool) string {
	if conv == "" {
		return source
	}
	body := fmt.Sprintf(conv, "$0")
	if !convThrows {
		return fmt.Sprintf("%s.map { %s }", source, body)
	}
	// The closure throws, so it needs a try of its own, and map rethrows, so
	// the call does too.
	return fmt.Sprintf("try %s.map { try %s }", source, body)
}

// swiftConvert returns a format string wrapping a lexical value in the
// conversion its type needs, and whether that conversion throws. The format
// never carries its own try: the callers above decide where try belongs.
func swiftConvert(m *ir.Model, f *ir.Field) (string, bool) {
	if f.TypeName != "" {
		if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum {
			return fmt.Sprintf("requireValue(%s.self, %%s)", f.TypeName), true
		}
		return "", false
	}
	switch f.Builtin {
	case ir.Bool:
		// This one cannot fail: anything that is not "true" or "1" is false,
		// which is what the schema says.
		return "toBool(%s)", false
	case ir.Decimal:
		return "requireDecimal(%s)", true
	case ir.Byte, ir.Short, ir.Int, ir.Long, ir.UnsignedByte, ir.UnsignedShort,
		ir.UnsignedInt, ir.UnsignedLong, ir.Float, ir.Double:
		return fmt.Sprintf("requireNumber(%s.self, %%s)", swiftBuiltin(f.Builtin)), true
	}
	return "", false
}

// swiftName escapes a member name that is a Swift keyword. Backticks make this
// lossless.
func swiftName(name string) string {
	if name == "" {
		return "value"
	}
	if swiftKeywords[name] {
		return "`" + name + "`"
	}
	return name
}

var swiftKeywords = map[string]bool{
	"associatedtype": true, "case": true, "class": true, "continue": true,
	"default": true, "defer": true, "deinit": true, "do": true, "else": true,
	"enum": true, "extension": true, "fallthrough": true, "false": true,
	"for": true, "func": true, "guard": true, "if": true, "import": true,
	"in": true, "init": true, "inout": true, "internal": true, "is": true,
	"let": true, "nil": true, "operator": true, "private": true,
	"protocol": true, "public": true, "repeat": true, "rethrows": true,
	"return": true, "self": true, "static": true, "struct": true,
	"subscript": true, "super": true, "switch": true, "throw": true,
	"throws": true, "true": true, "try": true, "typealias": true, "var": true,
	"where": true, "while": true, "as": true, "any": true, "break": true,
	"catch": true, "some": true,
}

// swiftDoc writes a documentation comment.
func swiftDoc(b *buf, indent, text string) {
	for _, line := range wrap(text, 72) {
		b.L("%s/// %s", indent, line)
	}
}

// swiftRuntime is the tiny DOM the readers walk, plus the conversions.
const swiftRuntime = `/// One element of a parsed document: its name, its attributes, its text and
/// its children. Foundation's tree-shaped XML API is not available on every
/// platform, but XMLParser is, so the tree is built here.
///
/// Not called XMLNode: Foundation already has one on Apple platforms, and a
/// schema is free to declare a type by that name too.
public final class XSDElement {
    public let name: String
    public let namespace: String?
    public let attributes: [String: String]
    public internal(set) var children: [XSDElement] = []
    /// The element's own character data, trimmed.
    public internal(set) var text: String = ""

    init(name: String, namespace: String?, attributes: [String: String]) {
        self.name = name
        self.namespace = namespace
        self.attributes = attributes
    }

    /// Direct children with this name, not descendants at any depth: the deep
    /// search would reach into nested elements of the same name and quietly
    /// duplicate content.
    public func children(_ name: String, namespace: String?) -> [XSDElement] {
        children.filter { $0.name == name && (namespace == nil || $0.namespace == namespace) }
    }

    public func child(_ name: String, namespace: String?) -> XSDElement? {
        children(name, namespace: namespace).first
    }

    public func childText(_ name: String, namespace: String?) -> String? {
        child(name, namespace: namespace)?.text
    }

    /// An unqualified attribute is not in the element's namespace, which is
    /// why the namespace is passed explicitly rather than inherited.
    public func attribute(_ name: String, namespace: String?) -> String? {
        attributes[name]
    }

    public func requireChild(_ name: String, namespace: String?) throws -> XSDElement {
        guard let found = child(name, namespace: namespace) else {
            throw XSDError.missingElement(name)
        }
        return found
    }

    public func requireText(_ name: String, namespace: String?) throws -> String {
        try requireChild(name, namespace: namespace).text
    }

    public func requireAttribute(_ name: String, namespace: String?) throws -> String {
        guard let value = attribute(name, namespace: namespace) else {
            throw XSDError.missingAttribute(name)
        }
        return value
    }
}

/// What can go wrong reading a document that does not match the schema.
public enum XSDError: Error, CustomStringConvertible {
    case invalidXML(String)
    case emptyDocument
    case unexpectedRoot(expected: String, found: String)
    case missingElement(String)
    case missingAttribute(String)
    case badValue(String, String)

    public var description: String {
        switch self {
        case .invalidXML(let detail): return "invalid XML: \(detail)"
        case .emptyDocument: return "the document is empty"
        case .unexpectedRoot(let expected, let found):
            return "expected a <\(expected)> document, got <\(found)>"
        case .missingElement(let name):
            return "the schema requires a <\(name)> element, and it is missing"
        case .missingAttribute(let name):
            return "the schema requires the \(name) attribute, and it is missing"
        case .badValue(let type, let value):
            return "\(value) is not a valid \(type)"
        }
    }
}

/// Parses a document into the node tree above.
public func parseDocument(_ data: Data) throws -> XSDElement {
    let builder = TreeBuilder()
    let parser = XMLParser(data: data)
    parser.shouldProcessNamespaces = true
    // A generated parser has no business resolving external entities: it is
    // the XXE hole, and no schema needs it.
    parser.shouldResolveExternalEntities = false
    parser.delegate = builder
    guard parser.parse() else {
        throw XSDError.invalidXML(parser.parserError?.localizedDescription ?? "unknown error")
    }
    guard let root = builder.root else {
        throw XSDError.emptyDocument
    }
    return root
}

/// The document element, checked against the name the schema declares for it.
public func rootElement(_ root: XSDElement, named name: String) throws -> XSDElement {
    guard root.name == name else {
        throw XSDError.unexpectedRoot(expected: name, found: root.name)
    }
    return root
}

private final class TreeBuilder: NSObject, XMLParserDelegate {
    var root: XSDElement?
    private var stack: [XSDElement] = []
    private var texts: [String] = []

    func parser(
        _ parser: XMLParser,
        didStartElement elementName: String,
        namespaceURI: String?,
        qualifiedName: String?,
        attributes: [String: String]
    ) {
        let node = XSDElement(name: elementName, namespace: namespaceURI, attributes: attributes)
        stack.last?.children.append(node)
        stack.append(node)
        texts.append("")
    }

    func parser(_ parser: XMLParser, foundCharacters string: String) {
        if !texts.isEmpty {
            texts[texts.count - 1] += string
        }
    }

    func parser(
        _ parser: XMLParser,
        didEndElement elementName: String,
        namespaceURI: String?,
        qualifiedName: String?
    ) {
        guard let node = stack.popLast() else { return }
        node.text = texts.popLast()?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if stack.isEmpty {
            root = node
        }
    }
}

func toBool(_ value: String) -> Bool { value == "true" || value == "1" }

/// An xs:list is one element or attribute holding many values.
func splitList(_ value: String?) -> [String] {
    guard let value = value else { return [] }
    return value.split(whereSeparator: { $0.isWhitespace }).map(String.init)
}

func requireValue<T: RawRepresentable>(_ type: T.Type, _ value: String) throws -> T
where T.RawValue == String {
    guard let parsed = T(rawValue: value) else {
        throw XSDError.badValue(String(describing: type), value)
    }
    return parsed
}

func requireNumber<T: LosslessStringConvertible>(_ type: T.Type, _ value: String) throws -> T {
    guard let parsed = T(value) else {
        throw XSDError.badValue(String(describing: type), value)
    }
    return parsed
}

/// Decimal has its own initializer rather than a retroactive conformance to
/// LosslessStringConvertible: conforming a type you do not own is a warning in
/// Swift 6 and the attribute that silences it does not parse on Swift 5.
func requireDecimal(_ value: String) throws -> Decimal {
    guard let parsed = Decimal(string: value) else {
        throw XSDError.badValue("Decimal", value)
    }
    return parsed
}`
