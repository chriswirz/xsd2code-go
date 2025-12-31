package gen

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genTypeScript emits TypeScript interfaces and the reader functions that turn
// a DOM element into one. JavaScript is the same generator with the type
// annotations moved into a .d.ts, so the two stay in step.
//
// The readers walk a DOM rather than parsing XML themselves: every browser and
// every JS runtime worth targeting either has DOMParser or can be handed a
// document by @xmldom/xmldom, and an XML parser written in generated code
// would be a liability nobody asked for.
func genTypeScript(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	body := tsBody(m, rel, o, true)
	return []File{{Name: "models.ts", Content: body}}, nil
}

// genJavaScript emits the same readers as plain ESM, plus a declaration file
// so a TypeScript consumer still gets the types.
func genJavaScript(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	return []File{
		{Name: "models.js", Content: tsBody(m, rel, o, false)},
		{Name: "models.d.ts", Content: tsDeclarations(m, o)},
	}, nil
}

// tsBody writes the module: types (when typed), the runtime helpers, and one
// reader per type.
func tsBody(m *ir.Model, rel *ir.Relational, o Options, typed bool) []byte {
	b := newBuf("  ")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	if ns := m.TargetNamespace; ns != "" {
		b.doc("// ", "The namespace every element of this schema lives in.")
		if typed {
			b.L("export const NAMESPACE: string | null = %q;", ns)
		} else {
			b.L("export const NAMESPACE = %q;", ns)
		}
	} else {
		b.doc("// ", "This schema is unqualified: its elements have no namespace.")
		b.L("export const NAMESPACE = null;")
	}
	b.L("")
	b.L("%s", tsRuntime(typed))

	if typed {
		for _, t := range m.Types {
			b.L("")
			tsType(b, m, rel, t)
		}
	} else {
		// The enum values are useful at runtime whether or not the module is
		// typed, so they are emitted as data rather than only as a type.
		for _, t := range m.Types {
			if t.Kind == ir.Enum {
				b.L("")
				tsEnumValues(b, t)
			}
		}
	}

	for _, t := range m.Types {
		if t.Kind == ir.Enum {
			continue
		}
		b.L("")
		tsReader(b, m, t, typed)
	}
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		b.L("")
		tsRoot(b, root, typed)
	}
	return b.Bytes()
}

// tsDeclarations writes the .d.ts that accompanies the JavaScript.
func tsDeclarations(m *ir.Model, o Options) []byte {
	b := newBuf("  ")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	b.L("export declare const NAMESPACE: string | null;")
	for _, t := range m.Types {
		b.L("")
		tsType(b, m, nil, t)
	}
	for _, t := range m.Types {
		if t.Kind == ir.Enum {
			continue
		}
		b.L("")
		b.L("export declare function read%s(el: Element): %s;", t.Name, t.Name)
	}
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		name := ir.Pascal(root.XMLName)
		b.L("")
		b.L("export declare function parse%s(xml: string): %s;", name, root.Type)
		b.L("export declare function from%sDocument(doc: Document): %s;", name, root.Type)
	}
	return b.Bytes()
}

// tsType writes one interface or union type.
func tsType(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type) {
	if t.Kind == ir.Enum {
		b.doc("// ", firstNonEmpty(t.Doc, fmt.Sprintf("The %q enumeration.", t.XMLName)))
		var members []string
		for _, v := range t.Values {
			members = append(members, fmt.Sprintf("%q", v.Value))
		}
		if len(members) == 0 {
			members = []string{"string"}
		}
		// A union of literals, rather than a TypeScript enum: it is the shape
		// the XML actually holds, it needs no runtime object, and it narrows
		// correctly when a value is compared.
		b.L("export type %s = %s;", t.Name, strings.Join(members, " | "))
		b.L("")
		tsEnumValues(b, t)
		return
	}

	doc := firstNonEmpty(t.Doc, fmt.Sprintf("The %q complex type.", t.XMLName))
	b.doc("// ", doc)
	if rel != nil {
		if tbl := rel.Table(t.Name); tbl != nil {
			b.doc("// ", fmt.Sprintf("Stored in the %q table.", tbl.Name))
		}
	}
	decl := "export interface " + t.Name
	if t.Base != "" {
		decl += " extends " + t.Base
	}
	b.L("%s {", decl)
	b.In()
	for _, f := range t.Fields {
		if d := fieldDoc(t, f); d != "" {
			b.doc("// ", d)
		}
		opt := ""
		if f.Optional && !f.Repeated {
			opt = "?"
		}
		b.L("%s%s: %s;", tsMember(f), opt, tsType_(m, f))
	}
	b.Out()
	b.L("}")
}

// tsEnumValues writes the runtime array of an enumeration's values.
func tsEnumValues(b *buf, t *ir.Type) {
	var members []string
	for _, v := range t.Values {
		members = append(members, fmt.Sprintf("%q", v.Value))
	}
	b.doc("// ", fmt.Sprintf("Every value %s may take, in the order the schema declares them.", t.Name))
	b.L("export const %sValues = [%s];", t.Name, strings.Join(members, ", "))
}

// tsReader writes the function that builds one object from a DOM element.
func tsReader(b *buf, m *ir.Model, t *ir.Type, typed bool) {
	b.doc("// ", fmt.Sprintf("Reads a %s from the element that contains it.", t.Name))
	if typed {
		b.L("export function read%s(el: Element): %s {", t.Name, t.Name)
	} else {
		b.doc("// ", "@param {Element} el")
		b.doc("// ", fmt.Sprintf("@returns {import(\"./models.js\").%s}", t.Name))
		b.L("export function read%s(el) {", t.Name)
	}
	b.In()
	if t.Base != "" {
		// The base's own members are read by its reader and spread in, so the
		// two never disagree about how a field is decoded.
		b.L("const out%s = read%s(el);", tsAny(typed), t.Base)
	} else {
		b.L("const out%s = {};", tsAny(typed))
	}
	for _, f := range t.Fields {
		b.L("out.%s = %s;", tsMember(f), tsRead(m, f))
	}
	if typed {
		// The assembled object is asserted rather than built in one literal,
		// because a derived type has to start from its base's result.
		b.L("return out as %s;", t.Name)
	} else {
		b.L("return out;")
	}
	b.Out()
	b.L("}")
}

// tsRoot writes the entry points for one document root.
func tsRoot(b *buf, root *ir.Root, typed bool) {
	name := ir.Pascal(root.XMLName)
	doc := fmt.Sprintf("Parses a %q document.", root.XMLName)
	if root.Doc != "" {
		doc += " " + root.Doc
	}
	b.doc("// ", doc)
	if typed {
		b.L("export function parse%s(xml: string): %s {", name, root.Type)
	} else {
		b.L("export function parse%s(xml) {", name)
	}
	b.In()
	b.L("return from%sDocument(parseDocument(xml));", name)
	b.Out()
	b.L("}")
	b.L("")
	b.doc("// ", fmt.Sprintf("Reads a %q document from an already-parsed DOM Document.", root.XMLName))
	if typed {
		b.L("export function from%sDocument(doc: Document): %s {", name, root.Type)
	} else {
		b.L("export function from%sDocument(doc) {", name)
	}
	b.In()
	b.L("const el = doc.documentElement;")
	b.L("if (!el || el.localName !== %q) {", root.XMLName)
	b.In()
	b.L("throw new Error(`expected a <%s> document, got <${el ? el.localName : \"nothing\"}>`);", root.XMLName)
	b.Out()
	b.L("}")
	b.L("return read%s(el);", root.Type)
	b.Out()
	b.L("}")
}

// tsAny annotates the accumulator the readers build into. TypeScript will not
// allow a property to be assigned onto an empty object literal, and the value
// is asserted to the real type when it is returned.
func tsAny(typed bool) string {
	if typed {
		return ": any"
	}
	return ""
}

// tsMember renders a member name. camelCase, which is what every JavaScript
// style guide and every consumer of this object expects.
func tsMember(f *ir.Field) string {
	return ir.Camel(f.Name)
}

// tsType_ renders the declared type of a field. The trailing underscore keeps
// it apart from the tsType function above, which writes a whole declaration.
func tsType_(m *ir.Model, f *ir.Field) string {
	switch f.Origin {
	case ir.AnyField:
		return "Element[]"
	case ir.AnyAttrField:
		return "Record<string, string>"
	}
	base := f.TypeName
	if base == "" {
		base = tsBuiltin(f.Builtin)
	}
	if f.Repeated || f.List {
		return base + "[]"
	}
	return base
}

func tsBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "boolean"
	case ir.Byte, ir.Short, ir.Int, ir.UnsignedByte, ir.UnsignedShort, ir.UnsignedInt, ir.Float, ir.Double:
		return "number"
	case ir.Long, ir.UnsignedLong:
		// Beyond 2^53 a JavaScript number silently loses precision, and an
		// xs:long is allowed to go there.
		return "number"
	case ir.Decimal:
		// Kept lexical: binary floating point cannot hold every xs:decimal,
		// and a money value that changes on a round trip is worse than one
		// that has to be parsed deliberately.
		return "string"
	case ir.DateTime:
		return "Date"
	}
	return "string"
}

// tsRead renders the expression that decodes one field.
func tsRead(m *ir.Model, f *ir.Field) string {
	switch f.Origin {
	case ir.AnyAttrField:
		return "readAnyAttributes(el)"
	case ir.AnyField:
		return "childElements(el, null, null)"
	case ir.TextField:
		return tsConvert(m, f, "textOf(el)", false)
	case ir.AttributeField:
		source := fmt.Sprintf("attrOf(el, %s, %q)", tsNS(f), f.XMLName)
		if f.List {
			return tsMapList(fmt.Sprintf("splitList(orEmpty(%s))", source), tsConvertFn(m, f))
		}
		return tsConvert(m, f, source, f.Optional)
	}

	// An element.
	if f.Repeated {
		if f.TypeName != "" && isClass(m, f.TypeName) {
			return fmt.Sprintf("childElements(el, %s, %q).map(read%s)", tsNS(f), f.XMLName, f.TypeName)
		}
		return fmt.Sprintf("childElements(el, %s, %q).map((c) => %s)",
			tsNS(f), f.XMLName, tsConvertNode(m, f))
	}
	if f.List {
		return tsMapList(fmt.Sprintf("splitList(orEmpty(childText(el, %s, %q)))", tsNS(f), f.XMLName),
			tsConvertFn(m, f))
	}
	if f.TypeName != "" && isClass(m, f.TypeName) {
		child := fmt.Sprintf("childElement(el, %s, %q)", tsNS(f), f.XMLName)
		if f.Optional {
			return fmt.Sprintf("mapElement(%s, read%s)", child, f.TypeName)
		}
		return fmt.Sprintf("read%s(required(%s, %q))", f.TypeName, child, f.XMLName)
	}
	return tsConvert(m, f, fmt.Sprintf("childText(el, %s, %q)", tsNS(f), f.XMLName), f.Optional)
}

// tsConvert wraps a string-valued expression in the conversion its type needs.
func tsConvert(m *ir.Model, f *ir.Field, source string, optional bool) string {
	fn := tsConvertFn(m, f)
	if fn == "" {
		if optional {
			// The interface spells an absent member as undefined; a DOM lookup
			// answers null, and two spellings of "absent" is one too many.
			return fmt.Sprintf("orUndefined(%s)", source)
		}
		return fmt.Sprintf("orEmpty(%s)", source)
	}
	if optional {
		return fmt.Sprintf("mapOptional(%s, %s)", source, fn)
	}
	return fmt.Sprintf("%s(orEmpty(%s))", fn, source)
}

// tsMapList converts each item of a split list, leaving the strings alone when
// the item type needs no conversion -- mapping identity over them would be
// noise, and an empty arrow body is not even valid.
func tsMapList(split, conv string) string {
	if conv == "" {
		return split
	}
	return fmt.Sprintf("%s.map((v) => %s(v))", split, conv)
}

// tsConvertNode converts the text of a repeated element's node.
func tsConvertNode(m *ir.Model, f *ir.Field) string {
	fn := tsConvertFn(m, f)
	if fn == "" {
		return "textOf(c)"
	}
	return fn + "(textOf(c))"
}

// tsConvertFn names the conversion for a field's type, or "" when the lexical
// string is already the value.
func tsConvertFn(m *ir.Model, f *ir.Field) string {
	if f.TypeName != "" {
		// An enumeration is a union of string literals: the lexical value is
		// the value, and only its membership is worth checking.
		return fmt.Sprintf("asEnumOf(%sValues, %q)", f.TypeName, f.TypeName)
	}
	switch f.Builtin {
	case ir.Bool:
		return "toBoolean"
	case ir.Byte, ir.Short, ir.Int, ir.Long, ir.UnsignedByte, ir.UnsignedShort,
		ir.UnsignedInt, ir.UnsignedLong, ir.Float, ir.Double:
		return "toNumber"
	case ir.DateTime:
		return "toDate"
	}
	return ""
}

// tsNS renders the namespace argument of a lookup.
func tsNS(f *ir.Field) string {
	if f.Namespace == "" {
		return "null"
	}
	return "NAMESPACE"
}

// isClass reports whether a named type is a complex type rather than an enum.
func isClass(m *ir.Model, name string) bool {
	t := m.Lookup(name)
	return t != nil && t.Kind == ir.Class
}

// tsRuntime is the small DOM helper library the readers are built on. It is
// emitted into the module rather than imported, so the generated file has no
// dependencies at all.
func tsRuntime(typed bool) string {
	src := tsRuntimeSource
	if typed {
		return src
	}
	// The JavaScript build is the TypeScript one with the annotations removed.
	// Doing it by substitution keeps one copy of the logic: two hand-written
	// copies would drift the first time either was fixed.
	for _, cut := range tsTypeAnnotations {
		src = strings.ReplaceAll(src, cut.typed, cut.plain)
	}
	return src
}

// tsTypeAnnotations maps each annotated form in the runtime to its plain
// JavaScript equivalent. Every TypeScript-only construct in the runtime source
// appears here; a generator test parses the JavaScript output to prove that
// nothing was missed.
var tsTypeAnnotations = []struct{ typed, plain string }{
	// The ambient declaration exists only to type a global the runtime does
	// not import; JavaScript needs no such statement, and typeof works on an
	// undeclared name.
	{"declare const DOMParser: { new (): { parseFromString(xml: string, type: string): Document } };\n\n", ""},
	{"(xml: string): Document", "(xml)"},
	{"(el: Element, ns: string | null, name: string | null): Element[]", "(el, ns, name)"},
	{"(el: Element, ns: string | null, name: string): Element | null", "(el, ns, name)"},
	{"(el: Element, ns: string | null, name: string): string | null", "(el, ns, name)"},
	{"(el: Element): Record<string, string>", "(el)"},
	{"(el: Element): string", "(el)"},
	{"(el: Element | null, name: string): Element", "(el, name)"},
	{"<T>(v: string | null, fn: (s: string) => T): T | undefined", "(v, fn)"},
	{"<T>(v: Element | null, fn: (e: Element) => T): T | undefined", "(v, fn)"},
	{"(v: string | null): string | undefined", "(v)"},
	{"(v: string | null): string", "(v)"},
	{"(v: string): boolean", "(v)"},
	{"(v: string): number", "(v)"},
	{"(v: string): Date", "(v)"},
	{"(v: string): string[]", "(v)"},
	{"(v: string, values: readonly string[], name: string): any", "(v, values, name)"},
	{"(values: readonly string[], name: string): (v: string) => any", "(values, name)"},
	{"return function (v: string) {", "return function (v) {"},
	{"const out: Element[] = []", "const out = []"},
	{"const out: Record<string, string> = {}", "const out = {}"},
	{"const n = el.childNodes[i] as Element;", "const n = el.childNodes[i];"},
}

const tsRuntimeSource = `// The DOM helpers the readers below are built on. They are emitted here rather
// than imported so this module has no dependencies.

declare const DOMParser: { new (): { parseFromString(xml: string, type: string): Document } };

// Parses a document with the platform\'s DOMParser. Browsers have one; Node does
// not, so on the server install @xmldom/xmldom and register it once:
//
//     import { DOMParser } from "@xmldom/xmldom";
//     globalThis.DOMParser = DOMParser;
export function parseDocument(xml: string): Document {
  if (typeof DOMParser === "undefined") {
    throw new Error(
      "no DOMParser: run in a browser, or set globalThis.DOMParser to the one " +
        "from @xmldom/xmldom",
    );
  }
  const doc = new DOMParser().parseFromString(xml, "application/xml");
  // Neither DOMParser reports a parse failure by throwing; both return a
  // document whose root is a parsererror element.
  const failure = doc.getElementsByTagName("parsererror")[0];
  if (failure) {
    throw new Error("invalid XML: " + (failure.textContent || "").trim());
  }
  return doc;
}

// Direct children matching a namespace and name; null for either matches any.
// Direct children, not getElementsByTagName, which would reach into nested
// elements of the same name and quietly duplicate content.
export function childElements(el: Element, ns: string | null, name: string | null): Element[] {
  const out: Element[] = [];
  // childNodes and nodeType rather than firstElementChild and
  // nextElementSibling: the element-only traversal is missing from some
  // server-side DOM implementations, @xmldom among them, and this pair is in
  // every one of them.
  for (let i = 0; i < el.childNodes.length; i++) {
    const n = el.childNodes[i] as Element;
    if (n.nodeType !== 1) continue;
    if (name !== null && n.localName !== name) continue;
    if (ns !== null && n.namespaceURI !== ns) continue;
    out.push(n);
  }
  return out;
}

export function childElement(el: Element, ns: string | null, name: string): Element | null {
  const all = childElements(el, ns, name);
  return all.length > 0 ? all[0] : null;
}

export function childText(el: Element, ns: string | null, name: string): string | null {
  const child = childElement(el, ns, name);
  return child === null ? null : textOf(child);
}

export function attrOf(el: Element, ns: string | null, name: string): string | null {
  // An unqualified attribute is not in the element\'s namespace, which is why
  // the namespace is passed explicitly rather than inherited.
  const v = ns === null ? el.getAttribute(name) : el.getAttributeNS(ns, name);
  return v === null || v === undefined ? null : v;
}

export function readAnyAttributes(el: Element): Record<string, string> {
  const out: Record<string, string> = {};
  for (let i = 0; i < el.attributes.length; i++) {
    const a = el.attributes[i];
    if (a.name === "xmlns" || a.name.indexOf("xmlns:") === 0) continue;
    out[a.name] = a.value;
  }
  return out;
}

export function textOf(el: Element): string {
  return (el.textContent || "").trim();
}

export function required(el: Element | null, name: string): Element {
  if (el === null) {
    throw new Error("the schema requires a <" + name + "> element, and it is missing");
  }
  return el;
}

export function mapOptional<T>(v: string | null, fn: (s: string) => T): T | undefined {
  return v === null || v === undefined ? undefined : fn(v);
}

export function mapElement<T>(v: Element | null, fn: (e: Element) => T): T | undefined {
  return v === null || v === undefined ? undefined : fn(v);
}

export function orUndefined(v: string | null): string | undefined {
  return v === null || v === undefined ? undefined : v;
}

export function orEmpty(v: string | null): string {
  return v === null || v === undefined ? "" : v;
}

export function toBoolean(v: string): boolean {
  return v === "true" || v === "1";
}

export function toNumber(v: string): number {
  const n = Number(v);
  if (Number.isNaN(n)) {
    throw new Error("expected a number, got " + JSON.stringify(v));
  }
  return n;
}

export function toDate(v: string): Date {
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) {
    throw new Error("expected an xs:dateTime, got " + JSON.stringify(v));
  }
  return d;
}

// An xs:list is one element or attribute holding many values.
export function splitList(v: string): string[] {
  const trimmed = orEmpty(v).trim();
  return trimmed === "" ? [] : trimmed.split(/\s+/);
}

export function asEnum(v: string, values: readonly string[], name: string): any {
  if (values.indexOf(v) < 0) {
    throw new Error(JSON.stringify(v) + " is not a value of " + name);
  }
  return v;
}

// The same check as a function of one argument, so it can be passed wherever a
// converter is expected.
export function asEnumOf(values: readonly string[], name: string): (v: string) => any {
  return function (v: string) {
    return asEnum(v, values, name);
  };
}
`
