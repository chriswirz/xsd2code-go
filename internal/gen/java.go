package gen

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genJava emits one file per type -- Java allows only one public type per file
// -- annotated for JAXB (jakarta.xml.bind) and, when persistence is on, for
// JPA (jakarta.persistence).
func genJava(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	pkg := o.Package
	if pkg == "" {
		pkg = "generated.models"
	}
	dir := strings.ReplaceAll(pkg, ".", "/") + "/"

	var files []File
	rootOwner := singleRootTypes(m)
	for _, t := range m.Types {
		var content []byte
		if t.Kind == ir.Enum {
			content = genJavaEnum(m, t, o, pkg)
		} else {
			content = genJavaClass(m, rel, t, o, pkg, rootOwner)
		}
		files = append(files, File{Name: dir + t.Name + ".java", Content: content})
	}
	files = append(files, File{Name: dir + "XmlDocuments.java",
		Content: genJavaDocuments(m, o, pkg)})
	files = append(files, File{Name: dir + "XsdAdapters.java",
		Content: []byte(javaAdaptersSource(pkg))})
	return files, nil
}

// javaPreamble writes the package declaration, the banner and the imports.
func javaPreamble(b *buf, o Options, pkg string, imports []string) {
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	b.L("package %s;", pkg)
	b.L("")
	for _, imp := range imports {
		b.L("import %s;", imp)
	}
	if len(imports) > 0 {
		b.L("")
	}
}

// genJavaEnum writes an enum plus the JPA converter that stores its XML
// lexical value. @Enumerated(STRING) would store the Java constant name, which
// is uppercase and frequently not what the document said.
func genJavaEnum(m *ir.Model, t *ir.Type, o Options, pkg string) []byte {
	b := newBuf("    ")
	javaPreamble(b, o, pkg, []string{
		"jakarta.xml.bind.annotation.XmlEnum",
		"jakarta.xml.bind.annotation.XmlEnumValue",
		"jakarta.xml.bind.annotation.XmlType",
		"jakarta.persistence.AttributeConverter",
		"jakarta.persistence.Converter",
	})
	javaDoc(b, "", t.Doc, fmt.Sprintf("The %q enumeration.", t.XMLName))
	b.L("@XmlType(name = %q, namespace = %q)", t.XMLName, t.Namespace)
	b.L("@XmlEnum")
	b.L("public enum %s {", t.Name)
	b.In()
	// Java spells its constants in upper snake case. The name is derived from
	// the lexical value rather than reused from the model, whose PascalCase
	// suits C# and Go.
	members := javaEnumNames(t)
	for i, v := range t.Values {
		if v.Doc != "" {
			javaDoc(b, "", v.Doc, "")
		}
		sep := ","
		if i == len(t.Values)-1 {
			sep = ";"
		}
		b.L("@XmlEnumValue(%q)", v.Value)
		b.L("%s(%q)%s", members[i], v.Value, sep)
	}
	if len(t.Values) == 0 {
		b.L(";")
	}
	b.L("")
	b.L("private final String value;")
	b.L("")
	b.L("%s(String value) {", t.Name)
	b.In()
	b.L("this.value = value;")
	b.Out()
	b.L("}")
	b.L("")
	javaDoc(b, "", "The lexical value this constant has in an XML document.", "")
	b.L("public String value() {")
	b.In()
	b.L("return value;")
	b.Out()
	b.L("}")
	b.L("")
	javaDoc(b, "", "Returns the constant with the given XML lexical value.", "")
	b.L("public static %s fromValue(String v) {", t.Name)
	b.In()
	b.L("for (%s c : %s.values()) {", t.Name, t.Name)
	b.In()
	b.L("if (c.value.equals(v)) {")
	b.In()
	b.L("return c;")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.L("throw new IllegalArgumentException(v);")
	b.Out()
	b.L("}")
	b.L("")
	javaDoc(b, "", "Stores the constant in the database as the value the document carried.", "")
	b.L("@Converter(autoApply = true)")
	b.L("public static class DbConverter implements AttributeConverter<%s, String> {", t.Name)
	b.In()
	b.L("@Override")
	b.L("public String convertToDatabaseColumn(%s attribute) {", t.Name)
	b.In()
	b.L("return attribute == null ? null : attribute.value();")
	b.Out()
	b.L("}")
	b.L("")
	b.L("@Override")
	b.L("public %s convertToEntityAttribute(String dbData) {", t.Name)
	b.In()
	b.L("return dbData == null ? null : %s.fromValue(dbData);", t.Name)
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")
	return b.Bytes()
}

// genJavaClass writes one complex type.
func genJavaClass(m *ir.Model, rel *ir.Relational, t *ir.Type, o Options, pkg string, rootOwner map[string]*ir.Root) []byte {
	var tbl *ir.Table
	if rel != nil {
		tbl = rel.Table(t.Name)
	}
	b := newBuf("    ")
	javaPreamble(b, o, pkg, javaImports(m, t, tbl))

	javaDoc(b, "", t.Doc, javaTypeFallbackDoc(t))
	// Field access, so the annotations sit next to the data and the getters
	// stay ordinary Java rather than part of the binding contract.
	b.L("@XmlAccessorType(XmlAccessType.FIELD)")
	if t.XMLName != "" {
		b.L("@XmlType(name = %q, namespace = %q)", t.XMLName, t.Namespace)
	} else {
		b.L("@XmlType(namespace = %q)", t.Namespace)
	}
	if root, ok := rootOwner[t.Name]; ok {
		b.L("@XmlRootElement(name = %q, namespace = %q)", root.XMLName, root.Namespace)
	}
	if tbl != nil {
		b.L("@Entity")
		b.L("@Table(name = %q)", tbl.Name)
		if t.Base == "" && len(subtypes(m, t.Name)) > 0 {
			// Joined inheritance matches the generated DDL, where a derived
			// table's primary key is a foreign key onto its base.
			b.L("@Inheritance(strategy = InheritanceType.JOINED)")
		}
	}
	decl := "public class " + t.Name
	if t.Abstract {
		decl = "public abstract class " + t.Name
	}
	if t.Base != "" {
		decl += " extends " + t.Base
	}
	b.L("%s {", decl)
	b.In()

	if tbl != nil && tbl.Surrogate && t.Base == "" {
		javaDoc(b, "", "Surrogate primary key. It belongs to the database, not to the document, so it is not serialized.", "")
		b.L("@Id")
		b.L("@GeneratedValue(strategy = GenerationType.IDENTITY)")
		b.L("@Column(name = %q)", tbl.Key().Name)
		b.L("@XmlTransient")
		b.L("private Long dbId;")
		b.L("")
	}

	for _, f := range t.Fields {
		genJavaField(b, m, tbl, t, f)
		b.L("")
	}

	if tbl != nil && tbl.Surrogate && t.Base == "" {
		javaAccessors(b, "Long", "dbId", "DbId")
		b.L("")
	}
	for i, f := range t.Fields {
		if i > 0 {
			b.L("")
		}
		javaAccessors(b, javaType(m, f), ir.Camel(f.Name), f.Name)
	}

	b.Out()
	b.L("}")
	return b.Bytes()
}

// genJavaField writes one member with its XML and persistence annotations.
func genJavaField(b *buf, m *ir.Model, tbl *ir.Table, t *ir.Type, f *ir.Field) {
	if d := fieldDoc(t, f); d != "" {
		javaDoc(b, "", d, "")
	}
	switch f.Origin {
	case ir.AttributeField:
		b.L("@XmlAttribute(name = %q%s%s)", f.XMLName, javaNS(f.Namespace), javaRequired(f))
	case ir.TextField:
		b.L("@XmlValue")
	case ir.AnyField:
		b.L("@XmlAnyElement(lax = true)")
	case ir.AnyAttrField:
		b.L("@XmlAnyAttribute")
	default:
		b.L("@XmlElement(name = %q%s%s%s)", f.XMLName, javaNS(f.Namespace),
			javaRequired(f), javaNillable(f))
	}
	if adapter := javaAdapter(f); adapter != "" {
		b.L("@XmlJavaTypeAdapter(%s.class)", adapter)
	}
	if f.List {
		b.L("@XmlList")
	}

	if tbl != nil {
		genJavaPersistence(b, m, tbl, f)
	}
	b.L("private %s %s%s;", javaType(m, f), ir.Camel(f.Name), javaInitializer(m, f))
}

// genJavaPersistence writes the JPA annotations for one field.
func genJavaPersistence(b *buf, m *ir.Model, tbl *ir.Table, f *ir.Field) {
	col := columnObj(tbl, f)
	switch {
	case col != nil && col.References != "":
		// Single-valued complex content is owned by its parent: cascading
		// means saving a document saves the whole tree in one call.
		b.L("@ManyToOne(cascade = CascadeType.ALL, fetch = FetchType.EAGER)")
		b.L("@JoinColumn(name = %q)", col.Name)
	case col == nil:
		var link *ir.LinkTable
		for _, l := range tbl.Links {
			if l.Field == f {
				link = l
			}
		}
		if link == nil {
			b.L("@Transient")
			return
		}
		b.L("@OneToMany(cascade = CascadeType.ALL, fetch = FetchType.EAGER)")
		b.L("@JoinTable(name = %q,", link.Name)
		b.L("        joinColumns = @JoinColumn(name = %q),", link.ParentColumn)
		b.L("        inverseJoinColumns = @JoinColumn(name = %q))", link.ChildColumn)
		// Document order is data. Without an order column the rows come back
		// in whatever order the planner chose.
		b.L("@OrderColumn(name = \"ordinal\")")
	default:
		if col.PrimaryKey {
			// The type's own key, so JPA is told which member it is instead
			// of being given a generated one it would have to invent.
			b.L("@Id")
		}
		if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Enum {
			b.L("@Convert(converter = %s.DbConverter.class)", f.TypeName)
		}
		b.L("@Column(name = %q, columnDefinition = %q%s)", col.Name, col.SQLType,
			javaNullable(col))
	}
}

func javaNullable(col *ir.Column) string {
	if col.NotNull {
		return ", nullable = false"
	}
	return ""
}

// javaAccessors writes a getter and setter pair.
func javaAccessors(b *buf, typ, field, name string) {
	prefix := "get"
	if typ == "boolean" {
		prefix = "is"
	}
	b.L("public %s %s%s() {", typ, prefix, name)
	b.In()
	b.L("return %s;", field)
	b.Out()
	b.L("}")
	b.L("")
	b.L("public void set%s(%s value) {", name, typ)
	b.In()
	b.L("this.%s = value;", field)
	b.Out()
	b.L("}")
}

// javaType renders the declared type of a field. The collection types are
// qualified when the schema declares a type of the same name, which would
// otherwise make every use of them ambiguous.
func javaType(m *ir.Model, f *ir.Field) string {
	list, mapType := "List", "Map"
	if m.Lookup("List") != nil {
		list = "java.util.List"
	}
	if m.Lookup("Map") != nil {
		mapType = "java.util.Map"
	}
	switch f.Origin {
	case ir.AnyField:
		return list + "<Object>"
	case ir.AnyAttrField:
		return mapType + "<javax.xml.namespace.QName, String>"
	}
	base := javaScalar(m, f)
	if f.Repeated || f.List {
		return list + "<" + base + ">"
	}
	return base
}

func javaScalar(m *ir.Model, f *ir.Field) string {
	if f.TypeName != "" {
		return f.TypeName
	}
	return javaBuiltin(f.Builtin)
}

// javaBuiltin maps a primitive. Boxed types throughout: a schema's minOccurs=0
// means a value can be absent, and a primitive int has no way to say so.
func javaBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "Boolean"
	case ir.Byte:
		return "Byte"
	case ir.Short, ir.UnsignedByte:
		return "Short"
	case ir.Int, ir.UnsignedShort:
		return "Integer"
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
	case ir.DateTime:
		return "java.time.OffsetDateTime"
	case ir.Date:
		return "java.time.LocalDate"
	case ir.Time:
		return "java.time.LocalTime"
	case ir.Duration:
		// xs:duration keeps months and days apart, which java.time.Duration
		// cannot; the lexical form is preserved and Postgres stores an
		// interval.
		return "String"
	case ir.Base64Binary, ir.HexBinary:
		return "byte[]"
	}
	return "String"
}

// javaAdapter names the XmlAdapter a field needs, if any. JAXB has no built-in
// binding for java.time, and hexBinary needs the non-default encoding.
func javaAdapter(f *ir.Field) string {
	if f.TypeName != "" {
		return ""
	}
	switch f.Builtin {
	case ir.DateTime:
		return "XsdAdapters.OffsetDateTimeAdapter"
	case ir.Date:
		return "XsdAdapters.LocalDateAdapter"
	case ir.Time:
		return "XsdAdapters.LocalTimeAdapter"
	case ir.HexBinary:
		return "jakarta.xml.bind.annotation.adapters.HexBinaryAdapter"
	}
	return ""
}

func javaInitializer(m *ir.Model, f *ir.Field) string {
	t := javaType(m, f)
	switch {
	case strings.HasPrefix(t, "List<"):
		return " = new ArrayList<>()"
	case strings.HasPrefix(t, "java.util.List<"):
		return " = new java.util.ArrayList<>()"
	case strings.HasPrefix(t, "Map<"):
		return " = new HashMap<>()"
	case strings.HasPrefix(t, "java.util.Map<"):
		return " = new java.util.HashMap<>()"
	}
	return ""
}

func javaNS(ns string) string {
	if ns == "" {
		return ""
	}
	return fmt.Sprintf(", namespace = %q", ns)
}

func javaRequired(f *ir.Field) string {
	if !f.Optional && !f.Repeated {
		return ", required = true"
	}
	return ""
}

func javaNillable(f *ir.Field) string {
	if f.Nillable {
		return ", nillable = true"
	}
	return ""
}

// javaImports collects the imports one generated file needs. Unused imports
// are legal but noisy, so each is added only when something asks for it.
func javaImports(m *ir.Model, t *ir.Type, tbl *ir.Table) []string {
	need := map[string]bool{
		"jakarta.xml.bind.annotation.XmlAccessType":   true,
		"jakarta.xml.bind.annotation.XmlAccessorType": true,
		"jakarta.xml.bind.annotation.XmlType":         true,
		"jakarta.xml.bind.annotation.XmlRootElement":  true,
	}
	for _, f := range t.Fields {
		switch f.Origin {
		case ir.AttributeField:
			need["jakarta.xml.bind.annotation.XmlAttribute"] = true
		case ir.TextField:
			need["jakarta.xml.bind.annotation.XmlValue"] = true
		case ir.AnyField:
			need["jakarta.xml.bind.annotation.XmlAnyElement"] = true
		case ir.AnyAttrField:
			need["jakarta.xml.bind.annotation.XmlAnyAttribute"] = true
		default:
			need["jakarta.xml.bind.annotation.XmlElement"] = true
		}
		if f.List {
			need["jakarta.xml.bind.annotation.XmlList"] = true
		}
		if javaAdapter(f) != "" {
			need["jakarta.xml.bind.annotation.adapters.XmlJavaTypeAdapter"] = true
		}
		typ := javaType(m, f)
		// Imported only when the schema has not claimed the name itself: an
		// import shadows nothing in Java, but a same-named type in this
		// package would make every use of it ambiguous to a reader and wrong
		// to the compiler.
		if strings.HasPrefix(typ, "java.util.") {
			continue
		}
		if strings.HasPrefix(typ, "List<") {
			need["java.util.List"] = true
			need["java.util.ArrayList"] = true
		}
		if strings.HasPrefix(typ, "Map<") {
			need["java.util.Map"] = true
			need["java.util.HashMap"] = true
		}
	}
	if tbl != nil {
		need["jakarta.xml.bind.annotation.XmlTransient"] = true
		for _, name := range []string{"Entity", "Table", "Id", "GeneratedValue",
			"GenerationType", "Column", "ManyToOne", "OneToMany", "JoinColumn",
			"JoinTable", "OrderColumn", "CascadeType", "FetchType", "Transient",
			"Convert", "Inheritance", "InheritanceType"} {
			need["jakarta.persistence."+name] = true
		}
	}
	out := make([]string, 0, len(need))
	for imp := range need {
		out = append(out, imp)
	}
	sortStrings(out)
	return out
}

// genJavaDocuments writes the JAXB entry points for the document roots.
func genJavaDocuments(m *ir.Model, o Options, pkg string) []byte {
	b := newBuf("    ")
	javaPreamble(b, o, pkg, []string{
		"jakarta.xml.bind.JAXBContext",
		"jakarta.xml.bind.JAXBException",
		"jakarta.xml.bind.Marshaller",
		"jakarta.xml.bind.Unmarshaller",
		"java.io.File",
		"java.io.InputStream",
		"java.io.OutputStream",
		"java.io.StringReader",
	})
	javaDoc(b, "", "Reads and writes the document roots the schema declares. The JAXBContext is built once: it is expensive to create and safe to share between threads.", "")
	b.L("public final class XmlDocuments {")
	b.In()
	b.L("private static final JAXBContext CONTEXT = createContext();")
	b.L("")
	b.L("private XmlDocuments() {")
	b.L("}")
	b.L("")
	b.L("private static JAXBContext createContext() {")
	b.In()
	b.L("try {")
	b.In()
	b.L("return JAXBContext.newInstance(%s);", javaContextClasses(m))
	b.Out()
	b.L("} catch (JAXBException e) {")
	b.In()
	b.L("throw new IllegalStateException(\"cannot build the JAXB context\", e);")
	b.Out()
	b.L("}")
	b.Out()
	b.L("}")

	for _, r := range m.Roots {
		if r.Type == "" {
			continue
		}
		name := ir.Pascal(r.XMLName)
		b.L("")
		javaDoc(b, "", strings.TrimSpace(fmt.Sprintf("Reads a %q document. %s", r.XMLName, r.Doc)), "")
		b.L("public static %s read%s(InputStream in) throws JAXBException {", r.Type, name)
		b.In()
		b.L("Unmarshaller unmarshaller = CONTEXT.createUnmarshaller();")
		b.L("return (%s) unmarshaller.unmarshal(in);", r.Type)
		b.Out()
		b.L("}")
		b.L("")
		javaDoc(b, "", fmt.Sprintf("Parses a %q document from a string.", r.XMLName), "")
		b.L("public static %s parse%s(String xml) throws JAXBException {", r.Type, name)
		b.In()
		b.L("Unmarshaller unmarshaller = CONTEXT.createUnmarshaller();")
		b.L("return (%s) unmarshaller.unmarshal(new StringReader(xml));", r.Type)
		b.Out()
		b.L("}")
		b.L("")
		javaDoc(b, "", fmt.Sprintf("Reads a %q document from a file.", r.XMLName), "")
		b.L("public static %s load%s(File file) throws JAXBException {", r.Type, name)
		b.In()
		b.L("Unmarshaller unmarshaller = CONTEXT.createUnmarshaller();")
		b.L("return (%s) unmarshaller.unmarshal(file);", r.Type)
		b.Out()
		b.L("}")
		b.L("")
		javaDoc(b, "", fmt.Sprintf("Writes a %q document.", r.XMLName), "")
		b.L("public static void write%s(%s value, OutputStream out) throws JAXBException {", name, r.Type)
		b.In()
		b.L("Marshaller marshaller = CONTEXT.createMarshaller();")
		b.L("marshaller.setProperty(Marshaller.JAXB_FORMATTED_OUTPUT, Boolean.TRUE);")
		b.L("marshaller.marshal(value, out);")
		b.Out()
		b.L("}")
	}
	b.Out()
	b.L("}")
	return b.Bytes()
}

// javaContextClasses lists the root classes the JAXBContext is built from.
func javaContextClasses(m *ir.Model) string {
	seen := map[string]bool{}
	var names []string
	for _, r := range m.Roots {
		if r.Type != "" && !seen[r.Type] {
			seen[r.Type] = true
			names = append(names, r.Type+".class")
		}
	}
	if len(names) == 0 {
		return "new Class<?>[0]"
	}
	return strings.Join(names, ", ")
}

// javaDoc writes a Javadoc block, falling back to the given text when the
// schema carried no documentation.
func javaDoc(b *buf, indent, text, fallback string) {
	if text == "" {
		text = fallback
	}
	if text == "" {
		return
	}
	b.L("%s/**", indent)
	for _, line := range wrap(escapeXMLDoc(text), 72) {
		b.L("%s * %s", indent, line)
	}
	b.L("%s */", indent)
}

func javaTypeFallbackDoc(t *ir.Type) string {
	if t.XMLName != "" {
		return fmt.Sprintf("The %q complex type.", t.XMLName)
	}
	return "An anonymous complex type from the schema."
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// javaEnumNames renders the constant names of an enum, keeping them unique:
// two values that differ only in punctuation would otherwise collide once both
// are upper-cased.
func javaEnumNames(t *ir.Type) []string {
	taken := map[string]bool{}
	out := make([]string, 0, len(t.Values))
	for _, v := range t.Values {
		name := ir.ScreamingSnake(v.Value)
		if name == "" {
			name = ir.ScreamingSnake(v.Name)
		}
		candidate := name
		for i := 2; taken[candidate]; i++ {
			candidate = fmt.Sprintf("%s_%d", name, i)
		}
		taken[candidate] = true
		out = append(out, candidate)
	}
	return out
}
