package gen

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genCSharp emits C# data classes annotated for System.Xml.Serialization, and
// -- when persistence is on -- for Entity Framework Core against Npgsql.
func genCSharp(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	ns := o.Package
	if ns == "" {
		ns = "Generated.Models"
	}

	b := newBuf("    ")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	b.L("using System;")
	b.L("using System.Collections.Generic;")
	b.L("using System.IO;")
	b.L("using System.Xml;")
	if o.XmlSerializable {
		b.L("using System.Xml.Schema;")
		b.L("using System.Xml.Serialization;")
		b.L("using System.Globalization;")
	} else {
		b.L("using System.Xml.Serialization;")
		if csharpUsesList(m) {
			b.L("using System.ComponentModel;")
			b.L("using System.Globalization;")
			b.L("using System.Linq;")
			b.L("using System.Reflection;")
		}
	}
	if rel != nil {
		b.L("using System.ComponentModel.DataAnnotations;")
		b.L("using System.ComponentModel.DataAnnotations.Schema;")
	}
	b.L("")
	b.L("namespace %s", ns)
	b.L("{")
	b.In()

	rootOwner := singleRootTypes(m)
	genCSharpRoots(b, m, o)

	for _, t := range m.Types {
		b.L("")
		if t.Kind == ir.Enum {
			genCSharpEnum(b, t, o)
			if o.XmlSerializable {
				b.L("")
				genCSharpXMLEnumHelper(b, t)
			}
			continue
		}
		genCSharpClass(b, m, rel, t, rootOwner, o)
		if o.XmlSerializable && len(csharpDescendants(m, t.Name)) > 0 {
			b.L("")
			genCSharpXMLFactory(b, m, t)
		}
	}

	if o.XmlSerializable {
		b.L("")
		b.L("%s", csharpXMLSupport)
	} else if csharpUsesList(m) {
		b.L("")
		b.L("%s", csharpListHelper)
	}

	b.Out()
	b.L("}")

	files := []File{{Name: "Models.cs", Content: b.Bytes()}}
	if rel != nil {
		files = append(files, File{Name: csharpContextName(ns) + ".cs",
			Content: []byte(genCSharpContext(m, rel, o, ns))})
	}
	return files, nil
}

// genCSharpRoots writes the static entry points: one pair of methods per
// document root, so a caller never has to construct an XmlSerializer.
func genCSharpRoots(b *buf, m *ir.Model, o Options) {
	var roots []*ir.Root
	for _, r := range m.Roots {
		if r.Type != "" {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		return
	}
	b.doc("/// ", "<summary>")
	b.doc("/// ", "Reads and writes the document roots the schema declares.")
	if o.XmlSerializable {
		b.doc("/// ", "Every method here drives XmlReader and XmlWriter through the types' own IXmlSerializable members, so nothing on this path uses reflection.")
	}
	b.doc("/// ", "</summary>")
	b.L("public static class XmlDocuments")
	b.L("{")
	b.In()
	for i, r := range roots {
		if i > 0 {
			b.L("")
		}
		name := ir.Pascal(r.XMLName)
		serializer := csharpSerializerExpr(r)
		b.doc("/// ", "<summary>")
		doc := fmt.Sprintf("Deserializes a %q document.", r.XMLName)
		if r.Doc != "" {
			doc += " " + r.Doc
		}
		b.doc("/// ", escapeXMLDoc(doc))
		b.doc("/// ", "</summary>")
		b.L("public static %s Read%s(Stream stream)", r.Type, name)
		b.L("{")
		b.In()
		if o.XmlSerializable {
			b.L("using var reader = XmlReader.Create(stream);")
			b.L("return Read%s(reader);", name)
		} else {
			b.L("var serializer = %s;", serializer)
			b.L("using var reader = XmlReader.Create(stream);")
			b.L("return (%s)serializer.Deserialize(reader);", r.Type)
		}
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", fmt.Sprintf("Deserializes a %q document from a string.", r.XMLName))
		b.doc("/// ", "</summary>")
		b.L("public static %s Parse%s(string xml)", r.Type, name)
		b.L("{")
		b.In()
		b.L("using var text = new StringReader(xml);")
		if o.XmlSerializable {
			b.L("using var reader = XmlReader.Create(text);")
			b.L("return Read%s(reader);", name)
		} else {
			b.L("var serializer = %s;", serializer)
			b.L("return (%s)serializer.Deserialize(text);", r.Type)
		}
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", fmt.Sprintf("Deserializes a %q document from a file.", r.XMLName))
		b.doc("/// ", "</summary>")
		b.L("public static %s Load%s(string path)", r.Type, name)
		b.L("{")
		b.In()
		b.L("using var stream = File.OpenRead(path);")
		b.L("return Read%s(stream);", name)
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", fmt.Sprintf("Serializes a %q document to a stream.", r.XMLName))
		b.doc("/// ", "</summary>")
		b.L("public static void Write%s(Stream stream, %s value)", name, r.Type)
		b.L("{")
		b.In()
		b.L("using var writer = XmlWriter.Create(stream, WriterSettings());")
		b.L("Write%s(writer, value);", name)
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", fmt.Sprintf("Serializes a %q document and returns it as XML text.", r.XMLName))
		b.doc("/// ", "</summary>")
		b.L("public static string ToXml%s(%s value)", name, r.Type)
		b.L("{")
		b.In()
		b.L("using var text = new Utf8StringWriter();")
		b.L("using (var writer = XmlWriter.Create(text, WriterSettings()))")
		b.L("{")
		b.In()
		b.L("Write%s(writer, value);", name)
		b.Out()
		b.L("}")
		b.L("return text.ToString();")
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", fmt.Sprintf("Serializes a %q document and writes it to a file, replacing anything already there.", r.XMLName))
		b.doc("/// ", "</summary>")
		b.L("public static void Save%s(string path, %s value)", name, r.Type)
		b.L("{")
		b.In()
		b.L("using var stream = File.Create(path);")
		b.L("Write%s(stream, value);", name)
		b.Out()
		b.L("}")
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", fmt.Sprintf("Serializes a %q document to an XmlWriter the caller owns.", r.XMLName))
		b.doc("/// ", "</summary>")
		b.L("public static void Write%s(XmlWriter writer, %s value)", name, r.Type)
		b.L("{")
		b.In()
		b.L("if (value == null)")
		b.L("{")
		b.In()
		b.L("throw new ArgumentNullException(nameof(value));")
		b.Out()
		b.L("}")
		if o.XmlSerializable {
			b.L("writer.WriteStartDocument();")
			b.L("writer.WriteStartElement(%q, %q);", r.XMLName, r.Namespace)
			b.L("XsdXml.DeclareInstanceNamespace(writer);")
			b.L("value.WriteXml(writer);")
			b.L("writer.WriteEndElement();")
			b.L("writer.WriteEndDocument();")
			b.L("writer.Flush();")
		} else {
			b.L("var serializer = %s;", serializer)
			b.L("serializer.Serialize(writer, value);")
			b.L("writer.Flush();")
		}
		b.Out()
		b.L("}")
		if o.XmlSerializable {
			b.L("")
			b.doc("/// ", "<summary>")
			b.doc("/// ", fmt.Sprintf("Deserializes a %q document from an XmlReader the caller owns.", r.XMLName))
			b.doc("/// ", "</summary>")
			b.L("public static %s Read%s(XmlReader reader)", r.Type, name)
			b.L("{")
			b.In()
			b.L("reader.MoveToContent();")
			b.L("var value = %s;", csharpXMLNew(m, r.Type, "reader"))
			b.L("value.ReadXml(reader);")
			b.L("return value;")
			b.Out()
			b.L("}")
		}
	}
	b.L("")
	b.doc("/// ", "<summary>")
	b.doc("/// ", "The writer settings the methods above use: indented, so a written document is readable, and without a byte order mark, which some readers of XML do not accept.")
	b.doc("/// ", "</summary>")
	b.L("private static XmlWriterSettings WriterSettings()")
	b.L("{")
	b.In()
	b.L("return new XmlWriterSettings")
	b.L("{")
	b.In()
	b.L("Indent = true,")
	b.L("Encoding = new System.Text.UTF8Encoding(false),")
	b.Out()
	b.L("};")
	b.Out()
	b.L("}")
	b.L("")
	b.doc("/// ", "<summary>")
	b.doc("/// ", "A StringWriter that reports UTF-8. StringWriter otherwise reports UTF-16, and the declaration on the returned text would then contradict the string a caller goes on to save as UTF-8.")
	b.doc("/// ", "</summary>")
	b.L("private sealed class Utf8StringWriter : StringWriter")
	b.L("{")
	b.In()
	b.L("public override System.Text.Encoding Encoding")
	b.L("{")
	b.In()
	b.L("get { return System.Text.Encoding.UTF8; }")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
}

// csharpSerializerExpr builds the XmlSerializer for a root. The root element
// name is supplied explicitly rather than relying on [XmlRoot], because one
// type may be the content of several differently named roots.
func csharpSerializerExpr(r *ir.Root) string {
	return fmt.Sprintf("new XmlSerializer(typeof(%s), new XmlRootAttribute { ElementName = %q, Namespace = %q })",
		r.Type, r.XMLName, r.Namespace)
}

// genCSharpEnum writes an enum whose members carry their XML lexical form.
func genCSharpEnum(b *buf, t *ir.Type, o Options) {
	b.doc("/// ", "<summary>")
	doc := t.Doc
	if doc == "" {
		doc = fmt.Sprintf("The %q enumeration.", t.XMLName)
	}
	b.doc("/// ", escapeXMLDoc(doc))
	b.doc("/// ", "</summary>")
	if t.XMLName != "" && !o.XmlSerializable {
		b.L("[XmlType(TypeName = %q, Namespace = %q)]", t.XMLName, t.Namespace)
	}
	b.L("public enum %s", t.Name)
	b.L("{")
	b.In()
	for i, v := range t.Values {
		if i > 0 {
			b.L("")
		}
		if v.Doc != "" {
			b.doc("/// ", "<summary>")
			b.doc("/// ", escapeXMLDoc(v.Doc))
			b.doc("/// ", "</summary>")
		}
		if o.XmlSerializable {
			// The lexical form lives in the generated converter instead, but a
			// reader of the enum still needs to see it.
			b.doc("/// ", fmt.Sprintf("<remarks>Written as %q.</remarks>", v.Value))
		} else {
			b.L("[XmlEnum(Name = %q)]", v.Value)
		}
		b.L("%s,", v.Name)
	}
	b.Out()
	b.L("}")
}

// genCSharpClass writes one complex type.
func genCSharpClass(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type, rootOwner map[string]*ir.Root, o Options) {
	b.doc("/// ", "<summary>")
	doc := t.Doc
	if doc == "" {
		if t.XMLName != "" {
			doc = fmt.Sprintf("The %q complex type.", t.XMLName)
		} else {
			doc = "An anonymous complex type from the schema."
		}
	}
	b.doc("/// ", escapeXMLDoc(doc))
	b.doc("/// ", "</summary>")

	if !o.XmlSerializable {
		if t.XMLName != "" {
			b.L("[XmlType(TypeName = %q, Namespace = %q)]", t.XMLName, t.Namespace)
		} else {
			b.L("[XmlType(Namespace = %q)]", t.Namespace)
		}
		if root, ok := rootOwner[t.Name]; ok {
			b.L("[XmlRoot(ElementName = %q, Namespace = %q)]", root.XMLName, root.Namespace)
		}
		// XmlSerializer refuses to write a base-class reference as a derived
		// instance unless it is told which derived types exist.
		for _, sub := range subtypes(m, t.Name) {
			b.L("[XmlInclude(typeof(%s))]", sub)
		}
	}
	var tbl *ir.Table
	if rel != nil {
		tbl = rel.Table(t.Name)
		if tbl != nil {
			b.L("[Table(%q)]", tbl.Name)
		}
	}

	decl := "public class " + t.Name
	if t.Abstract {
		decl = "public abstract class " + t.Name
	}
	switch {
	case t.Base != "":
		decl += " : " + t.Base
	case o.XmlSerializable:
		// Only the top of a hierarchy declares the interface; the members the
		// derived types contribute are overrides of the virtuals below.
		decl += " : IXmlSerializable"
	}
	b.L("%s", decl)
	b.L("{")
	b.In()

	// Every member name is claimed up front. A foreign key needs a property of
	// its own beside the navigation, and on a database whose column is named
	// "uploaded_by" rather than "uploaded_by_id" the obvious name for it is the
	// one the navigation already has.
	names := newNames()
	names.reserve(t.Name)
	names.reserve(csharpIDProperty)
	for _, f := range t.Fields {
		names.reserve(f.Name)
	}

	first := true
	if tbl != nil && tbl.Surrogate && t.Base == "" {
		b.doc("/// ", "<summary>")
		b.doc("/// ", "Surrogate primary key. It belongs to the database, not to the document, so it is never serialized.")
		b.doc("/// ", "</summary>")
		b.L("[Key]")
		b.L("[Column(%q)]", tbl.Key().Name)
		if !o.XmlSerializable {
			b.L("[XmlIgnore]")
		}
		b.L("public long %s { get; set; }", csharpIDProperty)
		first = false
	}

	for _, f := range t.Fields {
		if !first {
			b.L("")
		}
		first = false
		genCSharpProperty(b, m, tbl, t, f, names, o)
	}
	if o.XmlSerializable {
		genCSharpXMLMembers(b, m, t)
	}
	b.Out()
	b.L("}")
}

// csharpIDProperty is the surrogate key property name.
const csharpIDProperty = "DbId"

func genCSharpProperty(b *buf, m *ir.Model, tbl *ir.Table, t *ir.Type, f *ir.Field, names *nameSet, o Options) {
	// Settled before anything is written, because the [ForeignKey] attribute
	// on the navigation has to name the same property the declaration below
	// produces.
	fkName := ""
	if tbl != nil {
		if col := columnObj(tbl, f); col != nil && col.References != "" {
			fkName = csharpForeignKeyName(names, f, col)
		}
	}
	if d := fieldDoc(t, f); d != "" {
		b.doc("/// ", "<summary>")
		b.doc("/// ", escapeXMLDoc(d))
		b.doc("/// ", "</summary>")
	}
	if f.List {
		genCSharpListProperty(b, m, tbl, f, o)
		return
	}
	if !o.XmlSerializable {
		switch f.Origin {
		case ir.AttributeField:
			b.L("[XmlAttribute(AttributeName = %q%s)]", f.XMLName, csharpNS(f.Namespace))
		case ir.TextField:
			b.L("[XmlText]")
		case ir.AnyField:
			b.L("[XmlAnyElement]")
		case ir.AnyAttrField:
			b.L("[XmlAnyAttribute]")
		default:
			b.L("[XmlElement(ElementName = %q%s%s)]", f.XMLName, csharpNS(f.Namespace),
				nillableArg(f))
		}
	}

	typ := csharpType(m, f)
	if tbl != nil {
		switch col := columnObj(tbl, f); {
		case col != nil && col.References != "":
			// EF Core owns the foreign key; the navigation property itself is
			// not a column.
			b.L("[ForeignKey(%q)]", fkName)
		case col != nil:
			if col.PrimaryKey {
				// The type's own key: EF has to be told which member it is.
				b.L("[Key]")
			}
			b.L("[Column(%q, TypeName = %q)]", col.Name, col.SQLType)
			if col.NotNull && !f.Repeated {
				b.L("[Required]")
			}
		default:
			// Repeated complex content is a many-to-many through a link table.
			// It is left unannotated and configured in the DbContext instead:
			// [NotMapped] here would hide the navigation EF Core needs to see.
		}
	}
	b.L("public %s %s { get; set; }%s", typ, f.Name, csharpInitializer(typ))

	if usesSpecified(m, f) {
		b.L("")
		b.doc("/// ", "<summary>")
		b.doc("/// ", fmt.Sprintf("Whether %s was present. It is set when reading and consulted when writing, so an absent optional attribute does not reappear as its default value.", f.Name))
		b.doc("/// ", "</summary>")
		if !o.XmlSerializable {
			b.L("[XmlIgnore]")
		}
		if tbl != nil {
			// It is a serialization flag, not data: the column that backs the
			// value already records absence as NULL.
			b.L("[NotMapped]")
		}
		b.L("public bool %sSpecified { get; set; }", f.Name)
	}

	if tbl != nil {
		if col := columnObj(tbl, f); col != nil && col.References != "" {
			b.L("")
			b.doc("/// ", "<summary>")
			b.doc("/// ", fmt.Sprintf("Foreign key backing %s.", f.Name))
			b.doc("/// ", "</summary>")
			b.L("[Column(%q)]", col.Name)
			if !o.XmlSerializable {
				b.L("[XmlIgnore]")
			}
			b.L("public %s %s { get; set; }", csharpKeyType(col), fkName)
		}
	}
}

// genCSharpListProperty writes the pair of members an xs:list needs: the typed
// collection a caller works with, and the string XmlSerializer actually binds.
// There is no attribute that makes XmlSerializer split a whitespace-separated
// value into a collection, so the split is done here.
func genCSharpListProperty(b *buf, m *ir.Model, tbl *ir.Table, f *ir.Field, o Options) {
	item := csharpScalar(m, f)
	typ := "List<" + item + ">"

	if !o.XmlSerializable {
		b.L("[XmlIgnore]")
	}
	if tbl != nil {
		if col := columnObj(tbl, f); col != nil {
			// The collection is the mapped member: Npgsql binds a List<T>
			// straight onto a Postgres array.
			b.L("[Column(%q, TypeName = %q)]", col.Name, col.SQLType)
		}
	}
	b.L("public %s %s { get; set; } = new %s();", typ, f.Name, typ)
	if o.XmlSerializable {
		// The generated reader and writer split and join the value themselves,
		// so the string companion XmlSerializer needed has no purpose here.
		return
	}
	b.L("")
	b.doc("/// ", "<summary>")
	b.doc("/// ", fmt.Sprintf("The wire form of %s: its items separated by spaces. Bound by XmlSerializer; use %s instead.", f.Name, f.Name))
	b.doc("/// ", "</summary>")
	if f.Origin == ir.AttributeField {
		b.L("[XmlAttribute(AttributeName = %q%s)]", f.XMLName, csharpNS(f.Namespace))
	} else {
		b.L("[XmlElement(ElementName = %q%s)]", f.XMLName, csharpNS(f.Namespace))
	}
	b.L("[EditorBrowsable(EditorBrowsableState.Never)]")
	if tbl != nil {
		b.L("[NotMapped]")
	}
	b.L("public string %sSerialized", f.Name)
	b.L("{")
	b.In()
	b.L("get { return XsdList.Join(%s); }", f.Name)
	b.L("set { %s = XsdList.Parse<%s>(value); }", f.Name, item)
	b.Out()
	b.L("}")
}

// csharpForeignKeyName picks a free name for the property that holds a foreign
// key. The column's own name is the first choice, since that is what a reader
// of the database expects; when the navigation has already taken it, the
// navigation's name with Id appended is the next.
func csharpForeignKeyName(names *nameSet, f *ir.Field, col *ir.Column) string {
	candidate := ir.Pascal(col.Name)
	if names.has(candidate) {
		candidate = f.Name + "Id"
	}
	return names.take(candidate)
}

// csharpKeyType renders the CLR type of a foreign key column, which follows the
// key it references rather than being assumed to be an integer.
func csharpKeyType(col *ir.Column) string {
	base := "long"
	switch col.SQLType {
	case "text", "uuid", "character varying", "citext":
		// A referenced key need not be a number: a database keyed on a text
		// identifier is ordinary.
		return "string"
	case "integer", "smallint":
		base = "int"
	}
	if col.NotNull {
		return base
	}
	return base + "?"
}

// nillableArg adds IsNullable to an element attribute so that an absent value
// is written as xsi:nil rather than omitted, which is what a nillable element
// asks for.
func nillableArg(f *ir.Field) string {
	if f.Nillable {
		return ", IsNullable = true"
	}
	return ""
}

func csharpNS(ns string) string {
	if ns == "" {
		return ""
	}
	return fmt.Sprintf(", Namespace = %q", ns)
}

// csharpType renders the property type.
func csharpType(m *ir.Model, f *ir.Field) string {
	switch f.Origin {
	case ir.AnyField:
		return "XmlElement[]"
	case ir.AnyAttrField:
		return "XmlAttribute[]"
	}
	base := csharpScalar(m, f)
	if f.Repeated || f.List {
		return "List<" + base + ">"
	}
	if f.Optional && csharpIsValueType(m, f) {
		if usesSpecified(m, f) {
			// XmlSerializer refuses Nullable<T> on an attribute -- for every T,
			// enum and primitive alike -- so absence is carried by the
			// companion property instead.
			return base
		}
		// On an element a nullable value type is accepted, and it is the
		// clearer way to tell "absent" from "present and zero".
		return base + "?"
	}
	return base
}

// usesSpecified reports whether a member needs the XmlSerializer "Specified"
// companion rather than Nullable<T>.
//
// XmlSerializer will not bind a Nullable<T> to an attribute: it reports that
// XmlAttribute cannot encode a complex type, and it does so for int?, DateTime?
// and any enum? alike. The convention it does understand is a plain value plus
// a bool member named <Member>Specified, which is what xsd.exe generates.
func usesSpecified(m *ir.Model, f *ir.Field) bool {
	return f.Optional && f.Origin == ir.AttributeField && csharpIsValueType(m, f)
}

func csharpScalar(m *ir.Model, f *ir.Field) string {
	if f.TypeName != "" {
		return f.TypeName
	}
	return csharpBuiltin(f.Builtin)
}

// csharpIsValueType reports whether the scalar type needs Nullable<T> to be
// able to hold "absent".
func csharpIsValueType(m *ir.Model, f *ir.Field) bool {
	if f.TypeName != "" {
		t := m.Lookup(f.TypeName)
		return t != nil && t.Kind == ir.Enum
	}
	switch f.Builtin {
	case ir.String, ir.AnyURI, ir.QName, ir.AnyType, ir.Duration, ir.Base64Binary, ir.HexBinary:
		return false
	}
	return true
}

func csharpBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "bool"
	case ir.Byte:
		return "sbyte"
	case ir.Short:
		return "short"
	case ir.Int:
		return "int"
	case ir.Long:
		return "long"
	case ir.UnsignedByte:
		return "byte"
	case ir.UnsignedShort:
		return "ushort"
	case ir.UnsignedInt:
		return "uint"
	case ir.UnsignedLong:
		return "ulong"
	case ir.Float:
		return "float"
	case ir.Double:
		return "double"
	case ir.Decimal:
		return "decimal"
	case ir.DateTime, ir.Date:
		return "DateTime"
	case ir.Time:
		// TimeSpan is what XmlSerializer maps xs:time onto; DateTime would
		// invent a date the document never carried.
		return "TimeSpan"
	case ir.Duration:
		// xs:duration has no lossless CLR type: TimeSpan cannot hold months.
		// The lexical form is kept, and XmlConvert converts it on demand.
		return "string"
	case ir.Base64Binary, ir.HexBinary:
		return "byte[]"
	}
	return "string"
}

// csharpInitializer gives collections an empty instance, so a caller can add
// to a fresh object without a null check.
func csharpInitializer(typ string) string {
	if strings.HasPrefix(typ, "List<") {
		return " = new " + typ + "();"
	}
	return ""
}

// columnObj finds the column for a field.
func columnObj(tbl *ir.Table, f *ir.Field) *ir.Column {
	for _, c := range tbl.Columns {
		if c.Field == f {
			return c
		}
	}
	return nil
}

// subtypes lists the types that directly extend the named one.
func subtypes(m *ir.Model, name string) []string {
	var out []string
	for _, t := range m.Types {
		if t.Base == name {
			out = append(out, t.Name)
		}
	}
	return out
}

// escapeXMLDoc makes documentation safe inside an XML doc comment.
func escapeXMLDoc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// genCSharpContext writes an EF Core DbContext: table and column mapping, the
// joined inheritance strategy, and the link tables that carry repeated complex
// content.
func genCSharpContext(m *ir.Model, rel *ir.Relational, o Options, ns string) string {
	b := newBuf("    ")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	b.L("using Microsoft.EntityFrameworkCore;")
	b.L("")
	b.L("namespace %s", ns)
	b.L("{")
	b.In()
	name := csharpContextName(ns)
	b.doc("/// ", "<summary>")
	b.doc("/// ", "Entity Framework Core mapping for the generated types, against Npgsql.")
	b.doc("/// ", "Add it with services.AddDbContext&lt;"+name+"&gt;(o =&gt; o.UseNpgsql(connectionString)).")
	b.doc("/// ", "</summary>")
	b.L("public class %s : DbContext", name)
	b.L("{")
	b.In()
	b.L("public %s(DbContextOptions<%s> options) : base(options)", name, name)
	b.L("{")
	b.L("}")
	b.L("")
	for _, tbl := range rel.Tables {
		b.L("public DbSet<%s> %s { get; set; }", tbl.Type.Name, ir.Pascal(tbl.Type.Name)+"Set")
	}
	b.L("")
	b.L("protected override void OnModelCreating(ModelBuilder modelBuilder)")
	b.L("{")
	b.In()
	b.L("base.OnModelCreating(modelBuilder);")
	for _, tbl := range rel.Tables {
		b.L("")
		b.L("modelBuilder.Entity<%s>(entity =>", tbl.Type.Name)
		b.L("{")
		b.In()
		b.L("entity.ToTable(%q);", tbl.Name)
		if tbl.Parent == "" {
			// Table-per-type keeps each class's own columns in its own table,
			// which is what the generated DDL creates.
			b.L("entity.UseTptMappingStrategy();")
			key := tbl.Key()
			if tbl.Surrogate {
				b.L("entity.HasKey(e => e.%s);", csharpIDProperty)
				b.L("entity.Property(e => e.%s).HasColumnName(%q).ValueGeneratedOnAdd();",
					csharpIDProperty, key.Name)
			} else if key.Field != nil {
				b.L("entity.HasKey(e => e.%s);", key.Field.Name)
			}
		}
		for _, col := range tbl.Columns {
			if col.Field == nil || col.References != "" {
				continue
			}
			if col.Field.Repeated || col.Field.List {
				// Postgres arrays are mapped by Npgsql directly from the CLR
				// collection; no extra configuration needed.
				continue
			}
			b.L("entity.Property(e => e.%s).HasColumnName(%q).HasColumnType(%q);",
				col.Field.Name, col.Name, col.SQLType)
		}
		for _, link := range tbl.Links {
			b.L("entity.HasMany(e => e.%s).WithMany().UsingEntity(j => j.ToTable(%q));",
				link.Field.Name, link.Name)
		}
		b.Out()
		b.L("});")
	}
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	return b.String()
}

// csharpContextName derives the DbContext class name from the namespace.
func csharpContextName(ns string) string {
	last := ns
	if i := strings.LastIndex(ns, "."); i >= 0 {
		last = ns[i+1:]
	}
	return ir.Pascal(last) + "DbContext"
}

// csharpUsesList reports whether any field is an xs:list, so the helper below
// is emitted only when something needs it.
func csharpUsesList(m *ir.Model) bool {
	for _, t := range m.Types {
		for _, f := range t.Fields {
			if f.List {
				return true
			}
		}
	}
	return false
}

// csharpListHelper converts between an xs:list and a typed collection. The enum
// case goes through XmlEnum rather than the member name, because the two
// routinely differ: "on-hold" is not an identifier.
const csharpListHelper = `    /// <summary>
    /// Converts an xs:list -- many values held in one element or attribute,
    /// separated by whitespace -- to and from a typed collection.
    /// </summary>
    public static class XsdList
    {
        /// <summary>Splits a list value into its items.</summary>
        public static List<T> Parse<T>(string value)
        {
            var items = new List<T>();
            if (string.IsNullOrWhiteSpace(value))
            {
                return items;
            }
            foreach (var part in value.Split((char[])null, StringSplitOptions.RemoveEmptyEntries))
            {
                items.Add(Convert<T>(part));
            }
            return items;
        }

        /// <summary>Renders a collection as a list value, or null when empty
        /// so that an absent list is omitted rather than written as an empty
        /// element.</summary>
        public static string Join<T>(List<T> values)
        {
            if (values == null || values.Count == 0)
            {
                return null;
            }
            return string.Join(" ", values.Select(Render));
        }

        private static T Convert<T>(string item)
        {
            var type = typeof(T);
            if (type.IsEnum)
            {
                foreach (var field in type.GetFields(BindingFlags.Public | BindingFlags.Static))
                {
                    var attribute = field.GetCustomAttribute<XmlEnumAttribute>();
                    var name = attribute != null ? attribute.Name : field.Name;
                    if (name == item)
                    {
                        return (T)field.GetValue(null);
                    }
                }
                throw new FormatException(item + " is not a value of " + type.Name);
            }
            return (T)System.Convert.ChangeType(item, type, CultureInfo.InvariantCulture);
        }

        private static string Render<T>(T value)
        {
            var type = typeof(T);
            if (type.IsEnum)
            {
                var field = type.GetField(value.ToString());
                var attribute = field != null ? field.GetCustomAttribute<XmlEnumAttribute>() : null;
                return attribute != null ? attribute.Name : value.ToString();
            }
            return string.Format(CultureInfo.InvariantCulture, "{0}", value);
        }
    }`
