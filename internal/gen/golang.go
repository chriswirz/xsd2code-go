package gen

import (
	"fmt"
	"go/format"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

// genGo emits a Go package: one struct per complex type, one string-backed
// named type per enumeration, and a small support file carrying the XSD
// date/binary wrappers that encoding/xml has no native mapping for.
func genGo(m *ir.Model, rel *ir.Relational, o Options) ([]File, error) {
	pkg := o.Package
	if pkg == "" {
		pkg = "models"
	}

	b := newBuf("\t")
	for _, line := range header(o, "//") {
		b.L("%s", line)
	}
	b.L("")
	b.L("package %s", goPackageName(pkg))
	b.L("")
	b.L("import (")
	b.In()
	b.L(`"encoding/xml"`)
	b.L(`"fmt"`)
	b.L(`"io"`)
	b.L(`"os"`)
	b.Out()
	b.L(")")

	// Roots first: they are what a caller reaches for, and putting the entry
	// points at the top of the file saves the reader a search.
	rootOwner := singleRootTypes(m)
	for _, root := range m.Roots {
		genGoRoot(b, m, root)
	}

	for _, t := range m.Types {
		b.L("")
		switch t.Kind {
		case ir.Enum:
			genGoEnum(b, t)
		default:
			genGoStruct(b, m, rel, t, rootOwner)
		}
	}

	src, err := format.Source(b.Bytes())
	if err != nil {
		// A formatting failure means the generator emitted something invalid.
		// The unformatted text is far more useful than the error alone.
		return nil, fmt.Errorf("generated Go does not parse: %w\n%s", err, b.String())
	}
	files := []File{{Name: "models.go", Content: src}}

	support, err := format.Source([]byte(goSupportSource(goPackageName(pkg))))
	if err != nil {
		return nil, fmt.Errorf("support file does not parse: %w", err)
	}
	files = append(files, File{Name: "xsdtypes.go", Content: support})
	return files, nil
}

// genGoRoot writes the read helpers for one document root.
func genGoRoot(b *buf, m *ir.Model, root *ir.Root) {
	if root.Type == "" {
		return
	}
	name := ir.Pascal(root.XMLName)
	b.L("")
	// The schema's own documentation follows the generated sentence rather
	// than being spliced into it: it is prose of unknown shape and rarely fits
	// mid-sentence.
	doc := fmt.Sprintf("Unmarshal%s parses a %q document.", name, root.XMLName)
	if root.Doc != "" {
		doc += " " + root.Doc
	}
	b.doc("// ", doc)
	b.L("func Unmarshal%s(data []byte) (*%s, error) {", name, root.Type)
	b.In()
	b.L("var v %s", root.Type)
	b.L("if err := xml.Unmarshal(data, &v); err != nil {")
	b.In()
	b.L(`return nil, fmt.Errorf("unmarshal %s: %%w", err)`, root.XMLName)
	b.Out()
	b.L("}")
	b.L("return &v, nil")
	b.Out()
	b.L("}")
	b.L("")
	b.doc("// ", fmt.Sprintf("Read%s parses a %q document from a reader.", name, root.XMLName))
	b.L("func Read%s(r io.Reader) (*%s, error) {", name, root.Type)
	b.In()
	b.L("data, err := io.ReadAll(r)")
	b.L("if err != nil {")
	b.In()
	b.L("return nil, err")
	b.Out()
	b.L("}")
	b.L("return Unmarshal%s(data)", name)
	b.Out()
	b.L("}")
	b.L("")
	b.doc("// ", fmt.Sprintf("Load%s parses a %q document from a file.", name, root.XMLName))
	b.L("func Load%s(path string) (*%s, error) {", name, root.Type)
	b.In()
	b.L("data, err := os.ReadFile(path)")
	b.L("if err != nil {")
	b.In()
	b.L("return nil, err")
	b.Out()
	b.L("}")
	b.L("return Unmarshal%s(data)", name)
	b.Out()
	b.L("}")
}

// genGoEnum writes a string-backed named type plus its declared values. A
// named string type keeps the values self-documenting and still marshals as
// the plain lexical value the schema asks for.
func genGoEnum(b *buf, t *ir.Type) {
	doc := t.Doc
	if doc == "" {
		doc = fmt.Sprintf("%s is the %q enumeration.", t.Name, t.XMLName)
	} else {
		doc = t.Name + " " + lowerFirstWord(doc)
	}
	b.doc("// ", doc)
	b.L("type %s string", t.Name)
	if len(t.Values) == 0 {
		return
	}
	b.L("")
	b.doc("// ", fmt.Sprintf("The values %s may take.", t.Name))
	b.L("const (")
	b.In()
	for _, v := range t.Values {
		if v.Doc != "" {
			b.doc("// ", v.Doc)
		}
		b.L("%s%s %s = %q", t.Name, v.Name, t.Name, v.Value)
	}
	b.Out()
	b.L(")")
	b.L("")
	b.doc("// ", fmt.Sprintf("Valid reports whether v is one of the values %s declares.", t.Name))
	b.L("func (v %s) Valid() bool {", t.Name)
	b.In()
	b.L("switch v {")
	var names []string
	for _, val := range t.Values {
		names = append(names, t.Name+val.Name)
	}
	b.L("case %s:", strings.Join(names, ", "))
	b.In()
	b.L("return true")
	b.Out()
	b.L("}")
	b.L("return false")
	b.Out()
	b.L("}")
}

// genGoStruct writes one complex type.
func genGoStruct(b *buf, m *ir.Model, rel *ir.Relational, t *ir.Type, rootOwner map[string]*ir.Root) {
	doc := t.Doc
	if doc == "" {
		if t.XMLName != "" {
			doc = fmt.Sprintf("%s is the %q complex type.", t.Name, t.XMLName)
		} else {
			doc = fmt.Sprintf("%s is an anonymous complex type from the schema.", t.Name)
		}
	} else {
		doc = t.Name + " " + lowerFirstWord(doc)
	}
	b.doc("// ", doc)
	if t.Abstract {
		b.doc("// ", "The schema marks this type abstract: documents carry one of its extensions.")
	}
	b.L("type %s struct {", t.Name)
	b.In()

	if root, ok := rootOwner[t.Name]; ok {
		b.L("XMLName xml.Name `xml:%q`", xmlTagName(root.Namespace, root.XMLName))
	}
	if t.Base != "" {
		b.doc("// ", fmt.Sprintf("%s is the base type this one extends; its fields marshal inline.", t.Base))
		b.L("%s", t.Base)
	}
	var tbl *ir.Table
	if rel != nil {
		tbl = rel.Table(t.Name)
	}
	if tbl != nil && tbl.Surrogate && t.Base == "" {
		// Only a root of an inheritance chain owns the key; a derived type
		// reaches it through the embedded base, exactly as the joined tables do.
		// A type with a key of its own already has that field and needs none.
		b.doc("// ", "Surrogate primary key, populated by the database rather than the document.")
		b.L("%s int64 `xml:\"-\" json:\"-\" db:%q`", goIDField, tbl.Key().Name)
	}

	for _, f := range t.Fields {
		if d := fieldDoc(t, f); d != "" {
			b.doc("// ", d)
		}
		b.L("%s %s `%s`", f.Name, goType(m, f), goTags(m, tbl, f))
	}
	b.Out()
	b.L("}")
}

// goIDField is the member name of the surrogate key.
const goIDField = "DBID"

// goType renders the Go type of a field.
func goType(m *ir.Model, f *ir.Field) string {
	base := goScalar(m, f)
	switch {
	case f.Origin == ir.AnyAttrField:
		return "[]xml.Attr"
	case f.Origin == ir.AnyField:
		return "[]AnyElement"
	case f.List:
		// An xs:list is many values inside one element or attribute, which is
		// not the same thing as a repeated element and needs a type that knows
		// to split on whitespace.
		return "List[" + base + "]"
	case f.Repeated:
		return "[]" + base
	case f.Optional && !isNullableGo(base):
		// A pointer keeps "absent" and "present but zero" apart, which matters
		// as soon as the value is written back out or stored in a column that
		// distinguishes NULL from 0.
		return "*" + base
	}
	return base
}

// isNullableGo reports whether a Go type already has a natural empty value
// that round-trips as "absent", making a pointer pure friction.
func isNullableGo(t string) bool {
	return strings.HasPrefix(t, "[]") || strings.HasPrefix(t, "*")
}

func goScalar(m *ir.Model, f *ir.Field) string {
	if f.TypeName != "" {
		if t := m.Lookup(f.TypeName); t != nil && t.Kind == ir.Class {
			// Complex children are pointers: they may be large, they are
			// frequently absent, and a value member of a recursive type will
			// not compile.
			return "*" + f.TypeName
		}
		return f.TypeName
	}
	return goBuiltin(f.Builtin)
}

func goBuiltin(b ir.Builtin) string {
	switch b {
	case ir.Bool:
		return "bool"
	case ir.Byte:
		return "int8"
	case ir.Short:
		return "int16"
	case ir.Int:
		return "int32"
	case ir.Long:
		return "int64"
	case ir.UnsignedByte:
		return "uint8"
	case ir.UnsignedShort:
		return "uint16"
	case ir.UnsignedInt:
		return "uint32"
	case ir.UnsignedLong:
		return "uint64"
	case ir.Float:
		return "float32"
	case ir.Double:
		return "float64"
	case ir.Decimal:
		// xs:decimal is exact and unbounded. Binary floating point would round
		// it, so the lexical form is kept and handed to Postgres numeric
		// unchanged.
		return "Decimal"
	case ir.DateTime:
		return "DateTime"
	case ir.Date:
		return "Date"
	case ir.Time:
		return "Time"
	case ir.Duration:
		return "Duration"
	case ir.Base64Binary:
		return "Base64Binary"
	case ir.HexBinary:
		return "HexBinary"
	case ir.AnyType:
		return "string"
	}
	return "string"
}

// goTags renders the xml, json and db struct tags for a field.
func goTags(m *ir.Model, tbl *ir.Table, f *ir.Field) string {
	var xmlTag string
	switch f.Origin {
	case ir.AttributeField:
		xmlTag = xmlTagName(f.Namespace, f.XMLName) + ",attr"
		if f.Optional {
			xmlTag += ",omitempty"
		}
	case ir.TextField:
		xmlTag = ",chardata"
	case ir.AnyField:
		xmlTag = ",any"
	case ir.AnyAttrField:
		xmlTag = ",any,attr"
	default:
		xmlTag = xmlTagName(f.Namespace, f.XMLName)
		if f.Optional {
			xmlTag += ",omitempty"
		}
	}
	tags := []string{fmt.Sprintf("xml:%q", xmlTag)}

	// The document's own name, where there is one. ir.Snake would suffix a
	// name that collides with a SQL keyword, which means nothing in JSON.
	jsonTag := f.XMLName
	if jsonTag == "" {
		jsonTag = ir.Snake(f.Name)
	}
	if f.Optional {
		jsonTag += ",omitempty"
	}
	tags = append(tags, fmt.Sprintf("json:%q", jsonTag))

	if tbl != nil {
		if col := columnFor(tbl, f); col != "" {
			tags = append(tags, fmt.Sprintf("db:%q", col))
		} else {
			// Repeated complex content lives in a link table, so there is no
			// column to bind; the tag says so rather than binding the wrong one.
			tags = append(tags, `db:"-"`)
		}
	}
	return strings.Join(tags, " ")
}

// columnFor finds the column a field maps to, or "" when it has none.
func columnFor(tbl *ir.Table, f *ir.Field) string {
	for _, c := range tbl.Columns {
		if c.Field == f {
			return c.Name
		}
	}
	return ""
}

// xmlTagName renders the namespace-qualified name encoding/xml expects.
func xmlTagName(ns, local string) string {
	if ns != "" {
		return ns + " " + local
	}
	return local
}

// singleRootTypes maps a type name to the root element that uses it, but only
// when exactly one root does. An XMLName member pins a struct to one element
// name, which is a help for a type with a single root and a bug for a type
// reachable from several.
func singleRootTypes(m *ir.Model) map[string]*ir.Root {
	count := map[string]int{}
	for _, r := range m.Roots {
		if r.Type != "" {
			count[r.Type]++
		}
	}
	out := map[string]*ir.Root{}
	for _, r := range m.Roots {
		if r.Type != "" && count[r.Type] == 1 {
			out[r.Type] = r
		}
	}
	return out
}

// goPackageName sanitizes a package name: the last path segment, lowercased,
// with anything that is not an identifier character removed.
func goPackageName(s string) string {
	if i := strings.LastIndexAny(s, "/."); i >= 0 {
		s = s[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" || out[0] >= '0' && out[0] <= '9' {
		out = "models"
	}
	return out
}

// lowerFirstWord makes schema documentation read naturally after a Go doc
// comment's mandatory leading identifier.
func lowerFirstWord(s string) string {
	if s == "" {
		return s
	}
	fields := strings.Fields(s)
	first := fields[0]
	// An all-caps first word is an acronym and is left alone.
	if strings.ToUpper(first) != first {
		fields[0] = strings.ToLower(first[:1]) + first[1:]
	}
	return strings.Join(fields, " ")
}
