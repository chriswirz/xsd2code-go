package gen

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genCpp emits a header of structs and a unit of readers built on pugixml,
// which is the smallest XML library that is both ubiquitous and pleasant.
//
// C++ needs a type to be complete before it can be held by value, so the types
// are emitted in dependency order; anything left over after that is a cycle,
// and its members are held behind a shared_ptr.
func genCpp(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	ns := cppNamespace(o.Package)
	order, indirect := cppOrder(m)

	h := newBuf("    ")
	for _, line := range header(o, "//") {
		h.L("%s", line)
	}
	h.L("//")
	h.L("// Requires pugixml (https://pugixml.org): one header and one source file,")
	h.L("// or the pugixml package of any package manager.")
	h.L("")
	h.L("#pragma once")
	h.L("")
	h.L("#include <cstdint>")
	h.L("#include <memory>")
	h.L("#include <optional>")
	h.L("#include <stdexcept>")
	h.L("#include <string>")
	h.L("#include <vector>")
	h.L("")
	h.L("#include <pugixml.hpp>")
	h.L("")
	h.L("namespace %s {", ns)
	h.L("")
	if m.TargetNamespace != "" {
		h.doc("// ", "The XML namespace every element of this schema lives in.")
		h.L("inline constexpr const char* kNamespace = %q;", m.TargetNamespace)
		h.L("")
	}

	// Forward declarations, so a shared_ptr member can name a type that is
	// defined further down.
	h.L("// Forward declarations, so that recursive content can be expressed.")
	for _, t := range order {
		if t.Kind == ir.Class {
			h.L("struct %s;", t.Name)
		}
	}
	h.L("")

	for _, t := range order {
		if t.Kind == ir.Enum {
			genCppEnum(h, t)
		} else {
			genCppStruct(h, m, rel, t, indirect)
		}
		h.L("")
	}

	h.doc("// ", "Reads one value from the element that contains it. These are the pieces the document readers below are built from; a caller with an element already in hand can use them directly.")
	for _, t := range order {
		if t.Kind == ir.Class {
			h.L("%s read_%s(const pugi::xml_node& node);", t.Name, ir.Snake(t.Name))
		}
	}
	h.L("")
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		name := ir.Snake(ir.Pascal(root.XMLName))
		doc := fmt.Sprintf("Parses a %q document held in memory.", root.XMLName)
		if root.Doc != "" {
			doc += " " + root.Doc
		}
		h.doc("// ", doc)
		h.L("%s parse_%s(const std::string& xml);", root.Type, name)
		h.doc("// ", fmt.Sprintf("Parses a %q document from a file.", root.XMLName))
		h.L("%s load_%s(const std::string& path);", root.Type, name)
		h.L("")
	}
	h.L("} // namespace %s", ns)

	c := newBuf("    ")
	for _, line := range header(o, "//") {
		c.L("%s", line)
	}
	c.L("")
	c.L("#include <cctype>")
	c.L("#include <cstring>")
	c.L("#include <sstream>")
	c.L("")
	c.L("#include \"models.hpp\"")
	c.L("")
	c.L("namespace %s {", ns)
	c.L("namespace {")
	c.L("")
	c.L("%s", cppRuntime)
	c.L("} // namespace")
	c.L("")
	for _, t := range order {
		if t.Kind == ir.Enum {
			genCppEnumImpl(c, t)
			c.L("")
		}
	}
	for _, t := range order {
		if t.Kind != ir.Class {
			continue
		}
		genCppReader(c, m, t, indirect)
		c.L("")
	}
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		genCppRoot(c, root)
		c.L("")
	}
	c.L("} // namespace %s", ns)

	return []File{
		{Name: "models.hpp", Content: h.Bytes()},
		{Name: "models.cpp", Content: c.Bytes()},
	}, nil
}

// genCppEnum writes an enum class and the functions that convert it.
func genCppEnum(b *buf, t *ir.Type) {
	b.doc("// ", firstNonEmpty(t.Doc, fmt.Sprintf("The %q enumeration.", t.XMLName)))
	b.L("enum class %s {", t.Name)
	b.In()
	names := cppEnumNames(t)
	for i, v := range t.Values {
		if v.Doc != "" {
			b.doc("// ", v.Doc)
		}
		b.L("%s, // %q", names[i], v.Value)
	}
	b.Out()
	b.L("};")
	b.L("")
	b.doc("// ", fmt.Sprintf("The lexical value a %s has in a document.", t.Name))
	b.L("const char* to_string(%s value);", t.Name)
	b.doc("// ", fmt.Sprintf("Parses a %s, throwing std::runtime_error if the value is not one the schema declares.", t.Name))
	b.L("%s parse_%s(const std::string& value);", t.Name, ir.Snake(t.Name))
}

// genCppEnumImpl writes those conversions.
func genCppEnumImpl(b *buf, t *ir.Type) {
	names := cppEnumNames(t)
	b.L("const char* to_string(%s value) {", t.Name)
	b.In()
	b.L("switch (value) {")
	for i, v := range t.Values {
		b.L("case %s::%s: return %q;", t.Name, names[i], v.Value)
	}
	b.L("}")
	b.L("throw std::runtime_error(\"unknown %s\");", t.Name)
	b.Out()
	b.L("}")
	b.L("")
	b.L("%s parse_%s(const std::string& value) {", t.Name, ir.Snake(t.Name))
	b.In()
	for i, v := range t.Values {
		b.L("if (value == %q) return %s::%s;", v.Value, t.Name, names[i])
	}
	b.L("throw std::runtime_error(value + \" is not a value of %s\");", t.Name)
	b.Out()
	b.L("}")
}

// genCppStruct writes one complex type.
func genCppStruct(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type, indirect map[string]bool) {
	b.doc("// ", firstNonEmpty(t.Doc, fmt.Sprintf("The %q complex type.", t.XMLName)))
	if rel != nil {
		if tbl := rel.Table(t.Name); tbl != nil {
			b.doc("// ", fmt.Sprintf("Stored in the %q table.", tbl.Name))
		}
	}
	decl := "struct " + t.Name
	if t.Base != "" {
		decl += " : " + t.Base
	}
	b.L("%s {", decl)
	b.In()
	names := newNames()
	for _, f := range t.Fields {
		if d := fieldDoc(t, f); d != "" {
			b.doc("// ", d)
		}
		b.L("%s %s;", cppType(m, f, indirect), cppField(names.take(ir.Snake(f.Name))))
	}
	b.Out()
	b.L("};")
}

// genCppReader writes the function that fills one struct from a node.
func genCppReader(b *buf, m *ir.Model, t *ir.Type, indirect map[string]bool) {
	b.L("%s read_%s(const pugi::xml_node& node) {", t.Name, ir.Snake(t.Name))
	b.In()
	b.L("%s out;", t.Name)
	if t.Base != "" {
		// The base's reader owns its own fields, so the two cannot disagree
		// about how any of them is decoded.
		b.L("static_cast<%s&>(out) = read_%s(node);", t.Base, ir.Snake(t.Base))
	}
	names := newNames()
	for _, f := range t.Fields {
		name := cppField(names.take(ir.Snake(f.Name)))
		genCppField(b, m, f, name, indirect)
	}
	b.L("return out;")
	b.Out()
	b.L("}")
}

// genCppField writes the statements that read one member.
func genCppField(b *buf, m *ir.Model, f *ir.Field, name string, indirect map[string]bool) {
	isClassField := f.TypeName != "" && isClass(m, f.TypeName)

	switch {
	case f.Origin == ir.AnyAttrField:
		b.L("for (const auto& a : node.attributes()) {")
		b.In()
		b.L("out.%s.emplace_back(a.name(), a.value());", name)
		b.Out()
		b.L("}")
		return
	case f.Origin == ir.AnyField:
		b.L("for (const auto& c : node.children()) {")
		b.In()
		b.L("if (c.type() == pugi::node_element) out.%s.push_back(node_xml(c));", name)
		b.Out()
		b.L("}")
		return
	case f.Origin == ir.TextField:
		b.L("out.%s = %s;", name, cppConvert(m, f, "trim(node.child_value())"))
		return
	case f.Origin == ir.AttributeField:
		source := fmt.Sprintf("attr_of(node, %q)", f.XMLName)
		if f.List {
			b.L("for (const auto& v : split_list(%s.value_or(std::string()))) {", source)
			b.In()
			b.L("out.%s.push_back(%s);", name, cppConvertValue(m, f, "v"))
			b.Out()
			b.L("}")
			return
		}
		if f.Optional {
			b.L("if (auto v = %s) out.%s = %s;", source, name, cppConvertValue(m, f, "*v"))
			return
		}
		b.L("out.%s = %s;", name, cppConvert(m, f, fmt.Sprintf("require_attr(node, %q)", f.XMLName)))
		return
	}

	// A child element.
	if f.Repeated {
		b.L("for (const auto& c : node.children(%q)) {", f.XMLName)
		b.In()
		if isClassField {
			b.L("out.%s.push_back(read_%s(c));", name, ir.Snake(f.TypeName))
		} else {
			b.L("out.%s.push_back(%s);", name, cppConvertValue(m, f, "trim(c.child_value())"))
		}
		b.Out()
		b.L("}")
		return
	}
	if f.List {
		b.L("for (const auto& v : split_list(trim(node.child(%q).child_value()))) {", f.XMLName)
		b.In()
		b.L("out.%s.push_back(%s);", name, cppConvertValue(m, f, "v"))
		b.Out()
		b.L("}")
		return
	}
	if isClassField {
		b.L("if (const auto c = node.child(%q)) {", f.XMLName)
		b.In()
		if indirect[f.TypeName] || f.Optional {
			b.L("out.%s = std::make_shared<%s>(read_%s(c));", name, f.TypeName, ir.Snake(f.TypeName))
		} else {
			b.L("out.%s = read_%s(c);", name, ir.Snake(f.TypeName))
		}
		b.Out()
		if !f.Optional {
			b.L("} else {")
			b.In()
			b.L("throw std::runtime_error(\"the schema requires a <%s> element, and it is missing\");", f.XMLName)
			b.Out()
		}
		b.L("}")
		return
	}
	if f.Optional {
		b.L("if (const auto c = node.child(%q)) out.%s = %s;",
			f.XMLName, name, cppConvertValue(m, f, "trim(c.child_value())"))
		return
	}
	b.L("out.%s = %s;", name, cppConvert(m, f, fmt.Sprintf("trim(node.child(%q).child_value())", f.XMLName)))
}

// cppConvert and cppConvertValue wrap a string expression in the conversion a
// field's type needs.
func cppConvert(m *ir.Model, f *ir.Field, source string) string {
	return cppConvertValue(m, f, source)
}

func cppConvertValue(m *ir.Model, f *ir.Field, source string) string {
	if f.TypeName != "" {
		if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum {
			return fmt.Sprintf("parse_%s(%s)", ir.Snake(f.TypeName), source)
		}
	}
	switch f.Builtin {
	case ir.Bool:
		return fmt.Sprintf("to_bool(%s)", source)
	case ir.Byte, ir.Short, ir.Int, ir.Long:
		return fmt.Sprintf("to_int(%s)", source)
	case ir.UnsignedByte, ir.UnsignedShort, ir.UnsignedInt, ir.UnsignedLong:
		return fmt.Sprintf("to_uint(%s)", source)
	case ir.Float, ir.Double:
		return fmt.Sprintf("to_double(%s)", source)
	}
	return source
}

// genCppRoot writes the document entry points.
func genCppRoot(b *buf, root *ir.Root) {
	name := ir.Snake(ir.Pascal(root.XMLName))
	read := fmt.Sprintf("read_%s(root_element(doc, %q))", ir.Snake(root.Type), root.XMLName)
	b.L("%s parse_%s(const std::string& xml) {", root.Type, name)
	b.In()
	b.L("pugi::xml_document doc;")
	b.L("const auto result = doc.load_string(xml.c_str());")
	b.L("if (!result) throw std::runtime_error(std::string(\"invalid XML: \") + result.description());")
	b.L("return %s;", read)
	b.Out()
	b.L("}")
	b.L("")
	b.L("%s load_%s(const std::string& path) {", root.Type, name)
	b.In()
	b.L("pugi::xml_document doc;")
	b.L("const auto result = doc.load_file(path.c_str());")
	b.L("if (!result) throw std::runtime_error(path + \": \" + result.description());")
	b.L("return %s;", read)
	b.Out()
	b.L("}")
}

// cppType renders a member's type.
func cppType(m *ir.Model, f *ir.Field, indirect map[string]bool) string {
	switch f.Origin {
	case ir.AnyField:
		return "std::vector<std::string>"
	case ir.AnyAttrField:
		return "std::vector<std::pair<std::string, std::string>>"
	}
	base := f.TypeName
	if base == "" {
		base = cppBuiltin(f.Builtin)
	}
	isComplex := f.TypeName != "" && isClass(m, f.TypeName)
	switch {
	case f.Repeated, f.List:
		return "std::vector<" + base + ">"
	case isComplex && (indirect[f.TypeName] || f.Optional):
		// shared_ptr rather than optional for complex content: it doubles as
		// the indirection a recursive type needs, and an empty pointer says
		// "absent" just as clearly.
		return "std::shared_ptr<" + base + ">"
	case f.Optional:
		return "std::optional<" + base + ">"
	}
	return base
}

func cppBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "bool"
	case ir.Byte:
		return "std::int8_t"
	case ir.Short:
		return "std::int16_t"
	case ir.Int:
		return "std::int32_t"
	case ir.Long:
		return "std::int64_t"
	case ir.UnsignedByte:
		return "std::uint8_t"
	case ir.UnsignedShort:
		return "std::uint16_t"
	case ir.UnsignedInt:
		return "std::uint32_t"
	case ir.UnsignedLong:
		return "std::uint64_t"
	case ir.Float:
		return "float"
	case ir.Double:
		return "double"
	}
	return "std::string"
}

// cppOrder sorts the types so that every type is defined before it is held by
// value, and reports which ones a cycle forces behind a pointer.
func cppOrder(m *ir.Model) ([]*ir.Type, map[string]bool) {
	deps := map[string][]string{}
	byName := map[string]*ir.Type{}
	for _, t := range m.Types {
		byName[t.Name] = t
		if t.Kind != ir.Class {
			continue
		}
		if t.Base != "" {
			deps[t.Name] = append(deps[t.Name], t.Base)
		}
		for _, f := range t.Fields {
			// A vector's element type has to be complete too, and so does a
			// member held by value; a shared_ptr member does not.
			if f.TypeName != "" && !f.Optional {
				deps[t.Name] = append(deps[t.Name], f.TypeName)
			}
		}
	}

	var order []*ir.Type
	state := map[string]int{} // 0 unvisited, 1 in progress, 2 done
	indirect := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		switch state[name] {
		case 2:
			return
		case 1:
			// A cycle: the edge that closed it becomes a pointer, which is the
			// only way C++ can express the type at all.
			indirect[name] = true
			return
		}
		state[name] = 1
		for _, dep := range deps[name] {
			if dep != name {
				visit(dep)
			} else {
				indirect[name] = true
			}
		}
		state[name] = 2
		if t := byName[name]; t != nil {
			order = append(order, t)
		}
	}
	// Enums first: they depend on nothing and a struct may hold one.
	for _, t := range m.Types {
		if t.Kind == ir.Enum {
			state[t.Name] = 2
			order = append(order, t)
		}
	}
	for _, t := range m.Types {
		visit(t.Name)
	}
	return order, indirect
}

// cppEnumNames renders enumerator names, kept unique.
func cppEnumNames(t *ir.Type) []string {
	taken := map[string]bool{}
	out := make([]string, 0, len(t.Values))
	for _, v := range t.Values {
		name := ir.Pascal(v.Value)
		if name == "" {
			name = ir.Pascal(v.Name)
		}
		candidate := name
		for i := 2; taken[candidate]; i++ {
			candidate = fmt.Sprintf("%s%d", name, i)
		}
		taken[candidate] = true
		out = append(out, candidate)
	}
	return out
}

// cppField suffixes a member name that is a keyword.
func cppField(name string) string {
	if name == "" {
		return "value"
	}
	if cppKeywords[name] {
		return name + "_"
	}
	return name
}

var cppKeywords = map[string]bool{
	"alignas": true, "alignof": true, "and": true, "asm": true, "auto": true,
	"bitand": true, "bitor": true, "bool": true, "break": true, "case": true,
	"catch": true, "char": true, "class": true, "compl": true, "concept": true,
	"const": true, "consteval": true, "constexpr": true, "continue": true,
	"decltype": true, "default": true, "delete": true, "do": true, "double": true,
	"else": true, "enum": true, "explicit": true, "export": true, "extern": true,
	"false": true, "float": true, "for": true, "friend": true, "goto": true,
	"if": true, "inline": true, "int": true, "long": true, "mutable": true,
	"namespace": true, "new": true, "noexcept": true, "not": true, "nullptr": true,
	"operator": true, "or": true, "private": true, "protected": true,
	"public": true, "register": true, "requires": true, "return": true,
	"short": true, "signed": true, "sizeof": true, "static": true, "struct": true,
	"switch": true, "template": true, "this": true, "throw": true, "true": true,
	"try": true, "typedef": true, "typeid": true, "typename": true, "union": true,
	"unsigned": true, "using": true, "virtual": true, "void": true,
	"volatile": true, "while": true, "xor": true,
}

// cppNamespace turns a package name into a C++ namespace.
func cppNamespace(pkg string) string {
	if pkg == "" {
		return "models"
	}
	// A dotted or slashed name becomes nested namespaces, which C++17 can
	// spell in one declaration.
	parts := strings.FieldsFunc(pkg, func(r rune) bool { return r == '.' || r == '/' || r == ':' })
	for i, p := range parts {
		parts[i] = strings.ToLower(ir.Snake(p))
	}
	if len(parts) == 0 {
		return "models"
	}
	return strings.Join(parts, "::")
}

// cppRuntime is the anonymous-namespace helper set the readers use.
const cppRuntime = `// Trims the whitespace XML is free to add around any value.
std::string trim(const char* raw) {
    if (raw == nullptr) return std::string();
    std::string s(raw);
    const auto first = s.find_first_not_of(" \t\r\n");
    if (first == std::string::npos) return std::string();
    const auto last = s.find_last_not_of(" \t\r\n");
    return s.substr(first, last - first + 1);
}

// The part of a name after any namespace prefix. pugixml does not resolve
// namespaces, so a prefixed document is matched on the local name.
const char* local_name(const char* name) {
    const char* colon = std::strchr(name, ':');
    return colon == nullptr ? name : colon + 1;
}

std::optional<std::string> attr_of(const pugi::xml_node& node, const char* name) {
    const auto a = node.attribute(name);
    if (!a) return std::nullopt;
    return trim(a.value());
}

std::string require_attr(const pugi::xml_node& node, const char* name) {
    const auto a = node.attribute(name);
    if (!a) {
        throw std::runtime_error(std::string("the schema requires the ") + name + " attribute, and it is missing");
    }
    return trim(a.value());
}

bool to_bool(const std::string& v) { return v == "true" || v == "1"; }

std::int64_t to_int(const std::string& v) {
    try {
        return std::stoll(v);
    } catch (const std::exception&) {
        throw std::runtime_error("expected an integer, got \"" + v + "\"");
    }
}

std::uint64_t to_uint(const std::string& v) {
    try {
        return std::stoull(v);
    } catch (const std::exception&) {
        throw std::runtime_error("expected a non-negative integer, got \"" + v + "\"");
    }
}

double to_double(const std::string& v) {
    try {
        return std::stod(v);
    } catch (const std::exception&) {
        throw std::runtime_error("expected a number, got \"" + v + "\"");
    }
}

// An xs:list is one element or attribute holding many values.
std::vector<std::string> split_list(const std::string& v) {
    std::vector<std::string> out;
    std::size_t i = 0;
    while (i < v.size()) {
        while (i < v.size() && std::isspace(static_cast<unsigned char>(v[i]))) ++i;
        const auto start = i;
        while (i < v.size() && !std::isspace(static_cast<unsigned char>(v[i]))) ++i;
        if (i > start) out.push_back(v.substr(start, i - start));
    }
    return out;
}

// The document element, checked against the name the schema declares for it.
pugi::xml_node root_element(const pugi::xml_document& doc, const char* name) {
    const auto root = doc.document_element();
    if (!root) throw std::runtime_error("the document is empty");
    if (std::string(root.name()) != name && std::string(local_name(root.name())) != name) {
        throw std::runtime_error(std::string("expected a <") + name + "> document, got <" + root.name() + ">");
    }
    return root;
}

// Renders a wildcard-matched element back to text, since the schema says
// nothing about its shape and nothing can be assumed.
std::string node_xml(const pugi::xml_node& node) {
    std::ostringstream out;
    node.print(out, "", pugi::format_raw);
    return out.str();
}`
