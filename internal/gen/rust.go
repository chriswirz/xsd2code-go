package gen

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genRust emits structs deriving serde's traits, which quick-xml's serde
// support turns into a parser. It is the one XML binding in Rust that needs no
// generated parsing code at all: the derives are the parser.
func genRust(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	b := newBuf("    ")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("//")
	b.doc("// ", "Add to Cargo.toml:")
	b.L("//")
	b.L("//     [dependencies]")
	b.L("//     serde = { version = \"1\", features = [\"derive\"] }")
	b.L("//     quick-xml = { version = \"0.36\", features = [\"serialize\"] }")
	if rel != nil {
		b.L("//     # optional, for the sqlx feature the derives below sit behind:")
		b.L("//     sqlx = { version = \"0.8\", features = [\"postgres\", \"runtime-tokio\"] }")
	}
	b.L("")
	b.L("use serde::{Deserialize, Serialize};")
	b.L("")
	if usesList(m) {
		b.L("%s", rustListModule)
		b.L("")
	}

	// A struct that can contain itself has no size the compiler can work out.
	boxed := selfReferential(m)
	for _, t := range m.Types {
		if t.Kind == ir.Enum {
			genRustEnum(b, t)
		} else {
			genRustStruct(b, m, rel, t, boxed)
		}
		b.L("")
	}
	for _, root := range m.Roots {
		if root.Type == "" {
			continue
		}
		genRustRoot(b, root)
		b.L("")
	}
	return []File{{Name: "models.rs", Content: b.Bytes()}}, nil
}

// genRustEnum writes an enum whose variants carry their XML lexical value.
func genRustEnum(b *buf, t *ir.Type) {
	rustDoc(b, "", firstNonEmpty(t.Doc, fmt.Sprintf("The %q enumeration.", t.XMLName)))
	b.L("#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]")
	b.L("pub enum %s {", t.Name)
	b.In()
	names := rustEnumNames(t)
	for i, v := range t.Values {
		if v.Doc != "" {
			rustDoc(b, "", v.Doc)
		}
		if names[i] != v.Value {
			b.L("#[serde(rename = %q)]", v.Value)
		}
		b.L("%s,", names[i])
	}
	b.Out()
	b.L("}")
	if len(t.Values) == 0 {
		return
	}
	b.L("")
	b.L("impl %s {", t.Name)
	b.In()
	rustDoc(b, "", "The lexical value this variant has in an XML document.")
	b.L("pub fn as_str(&self) -> &'static str {")
	b.In()
	b.L("match self {")
	b.In()
	for i, v := range t.Values {
		b.L("%s::%s => %q,", t.Name, names[i], v.Value)
	}
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.L("")
	// Display and FromStr, so an enum works anywhere a lexical value is
	// expected -- inside an xs:list, above all.
	b.L("impl std::fmt::Display for %s {", t.Name)
	b.In()
	b.L("fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {")
	b.In()
	b.L("f.write_str(self.as_str())")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.L("")
	b.L("impl std::str::FromStr for %s {", t.Name)
	b.In()
	b.L("type Err = String;")
	b.L("")
	b.L("fn from_str(v: &str) -> Result<Self, Self::Err> {")
	b.In()
	b.L("match v {")
	b.In()
	for i, v := range t.Values {
		b.L("%q => Ok(%s::%s),", v.Value, t.Name, names[i])
	}
	b.L("other => Err(format!(\"{other:?} is not a value of %s\")),", t.Name)
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
}

// usesList reports whether any field is an xs:list, so the helper module is
// emitted only when something needs it.
func usesList(m *ir.Model) bool {
	for _, t := range m.Types {
		for _, f := range t.Fields {
			if f.List {
				return true
			}
		}
	}
	return false
}

// rustListModule serializes an xs:list: many values in one element or
// attribute, separated by whitespace.
const rustListModule = `/// Serializes and deserializes an xs:list: many values held in a single
/// element or attribute, separated by whitespace.
pub mod xsd_list {
    use serde::{Deserialize, Deserializer, Serializer};
    use std::fmt::Display;
    use std::str::FromStr;

    pub fn deserialize<'de, D, T>(deserializer: D) -> Result<Vec<T>, D::Error>
    where
        D: Deserializer<'de>,
        T: FromStr,
        T::Err: Display,
    {
        let raw = String::deserialize(deserializer)?;
        raw.split_whitespace()
            .map(|item| item.parse::<T>().map_err(serde::de::Error::custom))
            .collect()
    }

    pub fn serialize<S, T>(values: &[T], serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
        T: Display,
    {
        let joined: Vec<String> = values.iter().map(|v| v.to_string()).collect();
        serializer.serialize_str(&joined.join(" "))
    }
}`

// genRustStruct writes one complex type.
func genRustStruct(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type, boxed map[string]bool) {
	rustDoc(b, "", firstNonEmpty(t.Doc, fmt.Sprintf("The %q complex type.", t.XMLName)))
	var tbl *ir.Table
	if rel != nil {
		if tbl = rel.Table(t.Name); tbl != nil {
			rustDoc(b, "", fmt.Sprintf("Stored in the %q table.", tbl.Name))
		}
	}
	b.L("#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]")
	if tbl != nil {
		// Behind a feature, so a caller who only wants the XML side does not
		// have to depend on sqlx to compile this file.
		b.L("#[cfg_attr(feature = \"sqlx\", derive(sqlx::FromRow))]")
	}
	if t.XMLName != "" && t.XMLName != t.Name {
		b.L("#[serde(rename = %q)]", t.XMLName)
	}
	b.L("pub struct %s {", t.Name)
	b.In()

	names := newNames()
	// Rust has no inheritance, and quick-xml's deserializer does not support
	// serde's flatten, so an extension's inherited members are written out
	// here. The XML is identical to what an inheriting language produces; only
	// the Rust type is flat.
	fields := t.Fields
	if t.Base != "" {
		rustDoc(b, "", fmt.Sprintf("Inherited from %s, which this type extends.", t.Base))
		fields = append(inheritedFields(m, t.Base), t.Fields...)
	}
	for _, f := range fields {
		if d := fieldDoc(t, f); d != "" {
			rustDoc(b, "", d)
		}
		name := rustField(names.take(ir.Snake(f.Name)))
		serdeName := rustSerdeName(f)
		if serdeName != strings.TrimPrefix(name, "r#") {
			b.L("#[serde(rename = %q)]", serdeName)
		}
		var opts []string
		if f.List {
			// An xs:list is many values inside one element or attribute. serde
			// sees a single string; the helper module splits it.
			opts = append(opts, `with = "xsd_list"`)
		}
		if f.Optional || f.Repeated {
			// Absent content has to deserialize rather than fail, and it should
			// not reappear as an empty element when serialized back out.
			opts = append(opts, "default")
		}
		// An xs:list is a Vec too, so the skip has to follow the rendered type
		// rather than the Repeated flag alone.
		if f.Repeated || f.List {
			opts = append(opts, `skip_serializing_if = "Vec::is_empty"`)
		} else if f.Optional {
			opts = append(opts, `skip_serializing_if = "Option::is_none"`)
		}
		if len(opts) > 0 {
			b.L("#[serde(%s)]", strings.Join(opts, ", "))
		}
		b.L("pub %s: %s,", name, rustType(m, f, boxed))
	}
	b.Out()
	b.L("}")
}

// inheritedFields collects the fields of a base chain, outermost base first,
// so a derived struct declares them in the order a document presents them.
func inheritedFields(m *ir.Model, base string) []*ir.Field {
	t := m.Lookup(base)
	if t == nil {
		return nil
	}
	return append(inheritedFields(m, t.Base), t.Fields...)
}

// genRustRoot writes the parse and render helpers for one document root.
func genRustRoot(b *buf, root *ir.Root) {
	name := ir.Snake(ir.Pascal(root.XMLName))
	doc := fmt.Sprintf("Parses a %q document.", root.XMLName)
	if root.Doc != "" {
		doc += " " + root.Doc
	}
	rustDoc(b, "", doc)
	b.L("pub fn parse_%s(xml: &str) -> Result<%s, quick_xml::DeError> {", name, root.Type)
	b.In()
	b.L("quick_xml::de::from_str(xml)")
	b.Out()
	b.L("}")
	b.L("")
	rustDoc(b, "", fmt.Sprintf("Renders a %q document.", root.XMLName))
	b.L("pub fn write_%s(value: &%s) -> Result<String, quick_xml::DeError> {", name, root.Type)
	b.In()
	b.L("let mut out = String::new();")
	b.L("let ser = quick_xml::se::Serializer::with_root(&mut out, Some(%q))?;", root.XMLName)
	b.L("serde::Serialize::serialize(value, ser)?;")
	b.L("Ok(out)")
	b.Out()
	b.L("}")
}

// rustSerdeName renders the name serde matches on. quick-xml spells an
// attribute with a leading @ and the text content as $text, which is how one
// struct can describe both halves of an element.
func rustSerdeName(f *ir.Field) string {
	switch f.Origin {
	case ir.AttributeField:
		return "@" + f.XMLName
	case ir.TextField:
		return "$text"
	case ir.AnyField, ir.AnyAttrField:
		return "$value"
	}
	return f.XMLName
}

// rustType renders a field's type.
func rustType(m *ir.Model, f *ir.Field, boxed map[string]bool) string {
	base := f.TypeName
	if base == "" {
		base = rustBuiltin(f.Builtin)
	} else if boxed[f.TypeName] && !f.Repeated {
		// A struct that can contain itself has no size the compiler can work
		// out; the indirection is what makes it representable.
		base = "Box<" + base + ">"
	}
	switch {
	case f.Origin == ir.AnyAttrField:
		return "std::collections::HashMap<String, String>"
	case f.Repeated, f.List:
		return "Vec<" + base + ">"
	case f.Optional:
		return "Option<" + base + ">"
	}
	return base
}

func rustBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "bool"
	case ir.Byte:
		return "i8"
	case ir.Short:
		return "i16"
	case ir.Int:
		return "i32"
	case ir.Long:
		return "i64"
	case ir.UnsignedByte:
		return "u8"
	case ir.UnsignedShort:
		return "u16"
	case ir.UnsignedInt:
		return "u32"
	case ir.UnsignedLong:
		return "u64"
	case ir.Float:
		return "f32"
	case ir.Double:
		return "f64"
	}
	// Decimal, the temporal types and the binary types are kept lexical: each
	// would need a crate to hold it faithfully, and the string always
	// round-trips.
	return "String"
}

// rustEnumNames renders variant names in Rust's CamelCase, kept unique.
func rustEnumNames(t *ir.Type) []string {
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

// rustField escapes a field name that collides with a keyword. Rust's raw
// identifiers make this lossless: r#type is spelled differently and means
// exactly what "type" would have.
func rustField(name string) string {
	if name == "" {
		return "value"
	}
	switch name {
	case "self", "Self", "crate", "super":
		// These four cannot be raw identifiers at all, so they are renamed
		// rather than escaped.
		return name + "_"
	}
	if rustKeywords[name] {
		return "r#" + name
	}
	return name
}

var rustKeywords = map[string]bool{
	"as": true, "break": true, "const": true, "continue": true, "crate": true,
	"dyn": true, "else": true, "enum": true, "extern": true, "false": true,
	"fn": true, "for": true, "if": true, "impl": true, "in": true, "let": true,
	"loop": true, "match": true, "mod": true, "move": true, "mut": true,
	"pub": true, "ref": true, "return": true, "static": true, "struct": true,
	"super": true, "trait": true, "true": true, "type": true, "union": true,
	"unsafe": true, "use": true, "where": true, "while": true, "async": true,
	"await": true, "box": true, "abstract": true, "become": true, "do": true,
	"final": true, "macro": true, "override": true, "priv": true, "try": true,
	"typeof": true, "unsized": true, "virtual": true, "yield": true,
}

// rustDoc writes a /// documentation comment.
func rustDoc(b *buf, indent, text string) {
	for _, line := range wrap(text, 72) {
		b.L("%s/// %s", indent, line)
	}
}
