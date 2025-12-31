// Package ir holds the language-neutral model that sits between the parsed
// schema and the code generators. Everything awkward about XSD -- named and
// anonymous types, extension chains, group and attributeGroup references,
// substitution, occurrence ranges -- is resolved here exactly once, so a
// generator is a straightforward walk over classes and fields.
package ir

import "fmt"

// Builtin is a resolved XSD primitive. Each generator owns the mapping from a
// Builtin to a type in its language.
type Builtin int

// The primitives the generators know how to map. Anything not listed collapses
// to String, which always round-trips.
const (
	String Builtin = iota
	Bool
	Byte
	Short
	Int
	Long
	UnsignedByte
	UnsignedShort
	UnsignedInt
	UnsignedLong
	Float
	Double
	Decimal
	DateTime
	Date
	Time
	Duration
	Base64Binary
	HexBinary
	AnyURI
	QName
	AnyType
)

// String names the builtin for diagnostics.
func (b Builtin) String() string {
	if int(b) < len(builtinNames) {
		return builtinNames[b]
	}
	return "string"
}

var builtinNames = []string{
	"string", "boolean", "byte", "short", "int", "long",
	"unsignedByte", "unsignedShort", "unsignedInt", "unsignedLong",
	"float", "double", "decimal", "dateTime", "date", "time", "duration",
	"base64Binary", "hexBinary", "anyURI", "QName", "anyType",
}

// Numeric reports whether the builtin is a number, which decides whether a
// generator can safely emit a non-nullable value type.
func (b Builtin) Numeric() bool {
	switch b {
	case Byte, Short, Int, Long, UnsignedByte, UnsignedShort, UnsignedInt,
		UnsignedLong, Float, Double, Decimal:
		return true
	}
	return false
}

// Kind distinguishes the two things a generated type can be.
type Kind int

const (
	// Class is a complex type: a struct or class with fields.
	Class Kind = iota
	// Enum is a simple type restricted to an enumeration of string values.
	Enum
)

// Origin says where a field's value lives in the XML document, which is the
// single most important fact for emitting correct serialization attributes.
type Origin int

const (
	// ElementField is a child element.
	ElementField Origin = iota
	// AttributeField is an XML attribute.
	AttributeField
	// TextField is the element's own character data: the value half of an
	// xs:simpleContent extension, or the content of a mixed type.
	TextField
	// AnyField is an xs:any wildcard: unknown child elements, kept raw.
	AnyField
	// AnyAttrField is an xs:anyAttribute wildcard.
	AnyAttrField
)

// Model is everything the generators need: the types, plus the global elements
// that are legal document roots.
type Model struct {
	// TargetNamespace is the namespace of the primary schema.
	TargetNamespace string
	// Types are the classes and enums, in a stable order: the order the types
	// were declared, dependencies included.
	Types []*Type
	// Roots are the global element declarations. These are the entry points a
	// caller deserializes into.
	Roots []*Root
	// Warnings records everything that had to be approximated.
	Warnings []string

	byName map[string]*Type
	names  *uniquer
}

// Root is a global xs:element: a document root and the type it carries.
type Root struct {
	// XMLName is the element name as it appears in a document.
	XMLName string
	// Namespace is the element's namespace, empty for unqualified.
	Namespace string
	// Type is the name of the Type it deserializes into, empty when the root
	// carries a simple value.
	Type string
	// Builtin is the value type when Type is empty.
	Builtin Builtin
	// Doc is the element's xs:documentation.
	Doc string
}

// Type is one generated class or enum.
type Type struct {
	// Name is the generated identifier, in PascalCase and unique in the model.
	Name string
	// XMLName is the schema name this came from. It is empty for types
	// synthesized from an anonymous inline definition.
	XMLName string
	// Namespace is the target namespace of the schema that declared it.
	Namespace string
	Kind      Kind
	Doc       string

	// Base is the Name of the type this one extends, or "" for a root class.
	// Restriction-derived types are flattened instead of linked, because a
	// restriction cannot be modelled as inheritance in any target language.
	Base string
	// Abstract types are never instantiated directly; only their descendants
	// appear in documents.
	Abstract bool
	// Mixed types allow character data interleaved with child elements.
	Mixed bool

	// Fields is the content of a class, in document order: attributes and
	// elements as declared, base-class fields excluded.
	Fields []*Field

	// Table is the name the relational mapping should use for this type. It is
	// set when the model came from a database, where the table already exists
	// and its name is not ours to derive; empty means derive one.
	Table string
	// Key names the field that is this type's primary key. Empty means the
	// type has no natural key and the relational mapping adds a surrogate.
	Key string

	// Values is the set of members of an enum.
	Values []EnumValue
	// BaseBuiltin is the primitive an enum's values are written as; almost
	// always String.
	BaseBuiltin Builtin
}

// EnumValue is one xs:enumeration facet.
type EnumValue struct {
	// Name is the generated identifier.
	Name string
	// Value is the literal that appears in the XML.
	Value string
	Doc   string
}

// Field is one member of a class.
type Field struct {
	// Name is the generated member identifier, PascalCase; generators lower
	// the first letter where the language calls for it.
	Name string
	// XMLName is the attribute or element name in the document.
	XMLName string
	// Namespace is set only when the item is namespace-qualified, which is
	// what elementFormDefault="qualified" produces.
	Namespace string
	Doc       string
	Origin    Origin

	// TypeName names a Type in the model; when empty the field carries a
	// primitive given by Builtin.
	TypeName string
	Builtin  Builtin

	// Repeated is maxOccurs > 1: the field is a collection.
	Repeated bool
	// Optional is minOccurs = 0 or use="optional": the field may be absent,
	// and generators emit it as nullable so absent and zero stay distinct.
	Optional bool
	// Nillable elements may carry xsi:nil="true".
	Nillable bool
	// List marks an xs:list value: one attribute or element holding
	// space-separated items.
	List bool

	// Column is the name the relational mapping should use for this field, set
	// when the model came from a database. Empty means derive one, which is
	// what a field that came from a schema always does.
	Column string

	// Default and Fixed carry through the schema's default/fixed values.
	Default string
	Fixed   string

	// Choice is non-zero when the field belongs to an xs:choice; all fields
	// sharing a number are mutually exclusive. It is documentation, not
	// enforcement: no target language models a choice natively without
	// sacrificing the plain-object shape that makes deserialization simple.
	Choice int
}

// Lookup returns the named type, or nil.
func (m *Model) Lookup(name string) *Type {
	return m.byName[name]
}

// Warnf records an approximation for the caller to print.
func (m *Model) Warnf(format string, args ...any) {
	m.Warnings = append(m.Warnings, fmt.Sprintf(format, args...))
}

// NewModel returns an empty model that types can be added to. It is how a
// model gets built from something that is not an XML schema -- a database, for
// instance -- while keeping the uniqueness guarantee the generators rely on:
// no two types share a name, case-insensitively.
func NewModel(targetNamespace string) *Model {
	return &Model{
		TargetNamespace: targetNamespace,
		byName:          map[string]*Type{},
		names:           newUniquer(),
	}
}

// AddType registers a type, renaming it if its name is already taken, and
// returns it. The caller must use the returned Name when referring to it.
func (m *Model) AddType(t *Type) *Type {
	if m.names == nil {
		m.names = newUniquer()
		for _, existing := range m.Types {
			m.names.reserve(existing.Name)
		}
	}
	if m.byName == nil {
		m.byName = map[string]*Type{}
	}
	t.Name = m.names.take(t.Name)
	m.Types = append(m.Types, t)
	m.byName[t.Name] = t
	return t
}

// AddRoot declares a document root.
func (m *Model) AddRoot(r *Root) { m.Roots = append(m.Roots, r) }
