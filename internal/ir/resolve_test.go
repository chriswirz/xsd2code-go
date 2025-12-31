package ir

import (
	"strings"
	"testing"

	"github.com/chriswirz/xsd2code-go/internal/xsd"
)

// load parses one schema document from a literal, wrapping it in the schema
// element so the tests read as the fragments they are about.
func load(t *testing.T, body string) *Model {
	t.Helper()
	doc := `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"
           xmlns:tns="urn:test" targetNamespace="urn:test"
           elementFormDefault="qualified">` + body + `</xs:schema>`
	schema, err := xsd.Parse([]byte(doc), "test.xsd")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	set := &xsd.Set{Roots: []*xsd.Schema{schema}, All: []*xsd.Schema{schema}}
	return Build(set)
}

// fieldNames lists a type's fields in order, which is the property most of
// these tests are really about.
func fieldNames(t *Type) string {
	var names []string
	for _, f := range t.Fields {
		names = append(names, f.Name)
	}
	return strings.Join(names, ",")
}

func TestGroupExpandsInDocumentOrder(t *testing.T) {
	// A group reference contributes its members at the point of reference, not
	// after the elements declared beside it.
	m := load(t, `
	  <xs:group name="detail">
	    <xs:sequence>
	      <xs:element name="b" type="xs:string"/>
	      <xs:element name="c" type="xs:string"/>
	    </xs:sequence>
	  </xs:group>
	  <xs:complexType name="T">
	    <xs:sequence>
	      <xs:element name="a" type="xs:string"/>
	      <xs:group ref="tns:detail"/>
	      <xs:element name="d" type="xs:string"/>
	    </xs:sequence>
	  </xs:complexType>`)

	got := m.Lookup("T")
	if got == nil {
		t.Fatal("type T was not generated")
	}
	if names := fieldNames(got); names != "A,B,C,D" {
		t.Errorf("fields = %s, want A,B,C,D", names)
	}
}

func TestExtensionBecomesInheritance(t *testing.T) {
	m := load(t, `
	  <xs:complexType name="Base">
	    <xs:sequence><xs:element name="a" type="xs:string"/></xs:sequence>
	  </xs:complexType>
	  <xs:complexType name="Derived">
	    <xs:complexContent>
	      <xs:extension base="tns:Base">
	        <xs:sequence><xs:element name="b" type="xs:string"/></xs:sequence>
	      </xs:extension>
	    </xs:complexContent>
	  </xs:complexType>`)

	derived := m.Lookup("Derived")
	if derived.Base != "Base" {
		t.Fatalf("Base = %q, want Base", derived.Base)
	}
	// The base's own fields stay on the base: repeating them on the derived
	// type would shadow them in every target language.
	if names := fieldNames(derived); names != "B" {
		t.Errorf("derived fields = %s, want B", names)
	}
}

func TestRestrictionIsFlattened(t *testing.T) {
	// A restriction restates the content it keeps, so the derived type is
	// generated standalone rather than as a subclass that removes members.
	m := load(t, `
	  <xs:complexType name="Base">
	    <xs:sequence>
	      <xs:element name="a" type="xs:string"/>
	      <xs:element name="b" type="xs:string" minOccurs="0"/>
	    </xs:sequence>
	  </xs:complexType>
	  <xs:complexType name="Narrow">
	    <xs:complexContent>
	      <xs:restriction base="tns:Base">
	        <xs:sequence><xs:element name="a" type="xs:string"/></xs:sequence>
	      </xs:restriction>
	    </xs:complexContent>
	  </xs:complexType>`)

	narrow := m.Lookup("Narrow")
	if narrow.Base != "" {
		t.Errorf("Base = %q, want none", narrow.Base)
	}
	if names := fieldNames(narrow); names != "A" {
		t.Errorf("fields = %s, want A", names)
	}
}

func TestOccurrenceMapping(t *testing.T) {
	m := load(t, `
	  <xs:complexType name="T">
	    <xs:sequence>
	      <xs:element name="one" type="xs:string"/>
	      <xs:element name="maybe" type="xs:string" minOccurs="0"/>
	      <xs:element name="many" type="xs:string" maxOccurs="unbounded"/>
	      <xs:element name="maybeMany" type="xs:string" minOccurs="0" maxOccurs="5"/>
	    </xs:sequence>
	  </xs:complexType>`)

	fields := map[string]*Field{}
	for _, f := range m.Lookup("T").Fields {
		fields[f.Name] = f
	}
	if f := fields["One"]; f.Optional || f.Repeated {
		t.Errorf("One: optional=%v repeated=%v, want both false", f.Optional, f.Repeated)
	}
	if f := fields["Maybe"]; !f.Optional || f.Repeated {
		t.Errorf("Maybe: optional=%v repeated=%v, want optional", f.Optional, f.Repeated)
	}
	if f := fields["Many"]; !f.Repeated {
		t.Error("Many should be repeated")
	}
	// A repeated field is never also nullable: an empty collection already
	// says the element was absent.
	if f := fields["MaybeMany"]; !f.Repeated || f.Optional {
		t.Errorf("MaybeMany: optional=%v repeated=%v, want repeated only", f.Optional, f.Repeated)
	}
}

func TestChoiceMembersShareAGroupAndAreOptional(t *testing.T) {
	m := load(t, `
	  <xs:complexType name="T">
	    <xs:sequence>
	      <xs:element name="always" type="xs:string"/>
	      <xs:choice>
	        <xs:element name="a" type="xs:string"/>
	        <xs:element name="b" type="xs:string"/>
	      </xs:choice>
	    </xs:sequence>
	  </xs:complexType>`)

	fields := map[string]*Field{}
	for _, f := range m.Lookup("T").Fields {
		fields[f.Name] = f
	}
	if fields["Always"].Choice != 0 {
		t.Error("a field outside a choice should have no choice group")
	}
	a, b := fields["A"], fields["B"]
	if a.Choice == 0 || a.Choice != b.Choice {
		t.Errorf("choice groups = %d and %d, want equal and non-zero", a.Choice, b.Choice)
	}
	if !a.Optional || !b.Optional {
		t.Error("choice members must be optional: only one of them is present")
	}
}

func TestSimpleContentBecomesValuePlusAttributes(t *testing.T) {
	m := load(t, `
	  <xs:complexType name="Money">
	    <xs:simpleContent>
	      <xs:extension base="xs:decimal">
	        <xs:attribute name="currency" type="xs:string" use="required"/>
	      </xs:extension>
	    </xs:simpleContent>
	  </xs:complexType>`)

	money := m.Lookup("Money")
	if len(money.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(money.Fields))
	}
	if money.Fields[0].Origin != TextField || money.Fields[0].Builtin != Decimal {
		t.Errorf("value field = %+v", money.Fields[0])
	}
	if money.Fields[1].Origin != AttributeField || money.Fields[1].Optional {
		t.Errorf("currency should be a required attribute, got %+v", money.Fields[1])
	}
}

func TestEnumerationBecomesAnEnum(t *testing.T) {
	m := load(t, `
	  <xs:simpleType name="Status">
	    <xs:restriction base="xs:string">
	      <xs:enumeration value="on-hold"/>
	      <xs:enumeration value="shipped"/>
	    </xs:restriction>
	  </xs:simpleType>`)

	status := m.Lookup("Status")
	if status == nil || status.Kind != Enum {
		t.Fatalf("Status was not generated as an enum: %+v", status)
	}
	if status.Values[0].Name != "OnHold" || status.Values[0].Value != "on-hold" {
		t.Errorf("first value = %+v", status.Values[0])
	}
}

func TestListBecomesARepeatedPrimitive(t *testing.T) {
	m := load(t, `
	  <xs:simpleType name="Tags">
	    <xs:list itemType="xs:string"/>
	  </xs:simpleType>
	  <xs:complexType name="T">
	    <xs:sequence><xs:element name="tags" type="tns:Tags"/></xs:sequence>
	  </xs:complexType>`)

	f := m.Lookup("T").Fields[0]
	if !f.List || f.Builtin != String {
		t.Errorf("tags = %+v, want a string list", f)
	}
}

func TestRecursiveTypeTerminates(t *testing.T) {
	// A type that contains itself is ordinary in document schemas, and it must
	// not send the resolver into a loop.
	m := load(t, `
	  <xs:complexType name="Node">
	    <xs:sequence>
	      <xs:element name="child" type="tns:Node" minOccurs="0" maxOccurs="unbounded"/>
	    </xs:sequence>
	  </xs:complexType>`)

	node := m.Lookup("Node")
	if node == nil || node.Fields[0].TypeName != "Node" {
		t.Fatalf("recursive reference was not resolved: %+v", node)
	}
}

func TestGlobalElementBecomesARoot(t *testing.T) {
	m := load(t, `
	  <xs:element name="doc">
	    <xs:complexType>
	      <xs:sequence><xs:element name="a" type="xs:string"/></xs:sequence>
	    </xs:complexType>
	  </xs:element>`)

	if len(m.Roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(m.Roots))
	}
	root := m.Roots[0]
	if root.XMLName != "doc" || root.Namespace != "urn:test" || root.Type != "Doc" {
		t.Errorf("root = %+v", root)
	}
}

func TestQualifiedFormFollowsElementFormDefault(t *testing.T) {
	m := load(t, `
	  <xs:complexType name="T">
	    <xs:sequence><xs:element name="a" type="xs:string"/></xs:sequence>
	    <xs:attribute name="b" type="xs:string"/>
	  </xs:complexType>`)

	fields := m.Lookup("T").Fields
	if fields[0].Namespace != "urn:test" {
		t.Errorf("element namespace = %q, want urn:test", fields[0].Namespace)
	}
	// attributeFormDefault is unqualified unless a schema says otherwise, and
	// an unqualified attribute has no namespace at all.
	if fields[1].Namespace != "" {
		t.Errorf("attribute namespace = %q, want empty", fields[1].Namespace)
	}
}

func TestRelationsMapRepeatedComplexContentToALinkTable(t *testing.T) {
	m := load(t, `
	  <xs:complexType name="Item">
	    <xs:sequence><xs:element name="sku" type="xs:string"/></xs:sequence>
	  </xs:complexType>
	  <xs:complexType name="Order">
	    <xs:sequence>
	      <xs:element name="item" type="tns:Item" maxOccurs="unbounded"/>
	      <xs:element name="note" type="xs:string" minOccurs="0"/>
	    </xs:sequence>
	  </xs:complexType>`)

	rel := Relations(m, "")
	order := rel.Table("Order")
	if order == nil {
		t.Fatal("no table for Order")
	}
	if len(order.Links) != 1 || order.Links[0].Child != "item" {
		t.Fatalf("links = %+v, want one link to item", order.Links)
	}
	// The repeated complex field has no column of its own.
	for _, c := range order.Columns {
		if c.Field != nil && c.Field.Name == "Item" {
			t.Error("repeated complex content should not get a column")
		}
	}
	var note *Column
	for _, c := range order.Columns {
		if c.Name == "note" {
			note = c
		}
	}
	if note == nil || note.SQLType != "text" || note.NotNull {
		t.Errorf("note column = %+v, want a nullable text column", note)
	}
}

func TestRelationsUseJoinedInheritance(t *testing.T) {
	m := load(t, `
	  <xs:complexType name="Base">
	    <xs:sequence><xs:element name="a" type="xs:string"/></xs:sequence>
	  </xs:complexType>
	  <xs:complexType name="Derived">
	    <xs:complexContent>
	      <xs:extension base="tns:Base">
	        <xs:sequence><xs:element name="b" type="xs:int"/></xs:sequence>
	      </xs:extension>
	    </xs:complexContent>
	  </xs:complexType>`)

	rel := Relations(m, "app_")
	derived := rel.Table("Derived")
	if derived.Name != "app_derived" {
		t.Errorf("table name = %q, want app_derived", derived.Name)
	}
	if derived.Parent != "app_base" {
		t.Errorf("parent = %q, want app_base", derived.Parent)
	}
}
