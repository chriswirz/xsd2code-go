package infer

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// build folds the given documents into a schema and renders it.
func build(t *testing.T, opts Options, docs ...string) string {
	t.Helper()
	s := New(opts)
	for _, d := range docs {
		if err := s.Add(strings.NewReader(d)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	return s.XSD()
}

func TestOccurrenceIsCountedPerParent(t *testing.T) {
	// "item" appears twice in one parent and never in another: repeated and
	// optional. "name" appears exactly once in every parent: required and
	// single.
	xsd := build(t, DefaultOptions(),
		`<order><name>a</name><item>1</item><item>2</item></order>`,
		`<order><name>b</name></order>`)

	want := `<xs:element name="item" type="ItemType" minOccurs="0" maxOccurs="unbounded"/>`
	if !strings.Contains(xsd, want) {
		t.Errorf("item declaration missing from:\n%s", xsd)
	}
	if !strings.Contains(xsd, `<xs:element name="name" type="NameType"/>`) {
		t.Errorf("name should be required and single:\n%s", xsd)
	}
}

func TestAttributeUseFollowsPresence(t *testing.T) {
	xsd := build(t, DefaultOptions(),
		`<order id="1" note="x"/>`,
		`<order id="2"/>`)

	if !strings.Contains(xsd, `name="id" type="xs:long" use="required"`) {
		t.Errorf("id should be required:\n%s", xsd)
	}
	if strings.Contains(xsd, `name="note" type="xs:string" use="required"`) {
		t.Errorf("note should be optional:\n%s", xsd)
	}
}

func TestDatatypeInference(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"integer", `<r><v>42</v></r>`, "xs:long"},
		{"decimal", `<r><v>4.25</v></r>`, "xs:decimal"},
		{"date", `<r><v>2024-01-05</v></r>`, "xs:date"},
		{"dateTime", `<r><v>2024-01-05T08:00:00Z</v></r>`, "xs:dateTime"},
		{"time", `<r><v>08:00:00</v></r>`, "xs:time"},
		{"uri", `<r><v>https://example.com/a</v></r>`, "xs:anyURI"},
		{"text", `<r><v>hello</v></r>`, "xs:string"},
		// One value that is not a number demotes the whole field, which is the
		// point of subtractive inference.
		{"mixed", `<r><v>1</v><v>2</v><v>n/a</v></r>`, "xs:string"},
		// 0 and 1 are valid booleans, but a column of them is an integer far
		// more often than a flag.
		{"digits are not booleans", `<r><v>0</v><v>1</v></r>`, "xs:long"},
		{"spelled booleans", `<r><v>true</v><v>false</v></r>`, "xs:boolean"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			xsd := build(t, DefaultOptions(), c.doc)
			want := `<xs:restriction base="` + c.want + `"`
			if !strings.Contains(xsd, want) {
				t.Errorf("want %s in:\n%s", want, xsd)
			}
		})
	}
}

func TestStringsOptionSuppressesInference(t *testing.T) {
	opts := DefaultOptions()
	opts.Strings = true
	xsd := build(t, opts, `<r><v>42</v></r>`)
	if !strings.Contains(xsd, `<xs:restriction base="xs:string"/>`) {
		t.Errorf("want everything as a string:\n%s", xsd)
	}
}

func TestEnumerationInference(t *testing.T) {
	opts := DefaultOptions()
	opts.MinEnumSamples = 4
	doc := `<r>
	  <s>active</s><s>active</s><s>held</s><s>held</s><s>closed</s>
	</r>`
	xsd := build(t, opts, doc)
	for _, v := range []string{"active", "held", "closed"} {
		if !strings.Contains(xsd, `<xs:enumeration value="`+v+`"/>`) {
			t.Errorf("missing enumeration %q in:\n%s", v, xsd)
		}
	}
}

func TestUniqueValuesAreNotAnEnumeration(t *testing.T) {
	// Every value distinct means an identifier, not a closed set, however few
	// samples there are.
	opts := DefaultOptions()
	opts.MinEnumSamples = 2
	xsd := build(t, opts, `<r><id>a</id><id>b</id><id>c</id></r>`)
	if strings.Contains(xsd, "xs:enumeration") {
		t.Errorf("distinct values should not become an enumeration:\n%s", xsd)
	}
}

func TestVaryingChildOrderBecomesAChoice(t *testing.T) {
	xsd := build(t, DefaultOptions(),
		`<r><a>1</a><b>2</b></r>`,
		`<r><b>2</b><a>1</a></r>`)

	if !strings.Contains(xsd, `<xs:choice maxOccurs="unbounded">`) {
		t.Errorf("want a repeated choice:\n%s", xsd)
	}
	// Inside a choice every member has to be optional: a choice matches one.
	if !strings.Contains(xsd, `<xs:element name="a" type="AType" minOccurs="0"/>`) {
		t.Errorf("choice members must be optional:\n%s", xsd)
	}
}

func TestConsistentOrderStaysASequence(t *testing.T) {
	// The second document omits a middle element. That is consistent with the
	// order, so it must not force a choice.
	xsd := build(t, DefaultOptions(),
		`<r><a>1</a><b>2</b><c>3</c></r>`,
		`<r><a>1</a><c>3</c></r>`)

	if strings.Contains(xsd, "xs:choice") {
		t.Errorf("a subsequence is still a sequence:\n%s", xsd)
	}
}

func TestNamespaceBecomesTheTargetNamespace(t *testing.T) {
	xsd := build(t, DefaultOptions(), `<r xmlns="urn:example"><a>1</a></r>`)
	if !strings.Contains(xsd, `targetNamespace="urn:example"`) {
		t.Errorf("want the document namespace as the target:\n%s", xsd)
	}
	if !strings.Contains(xsd, `elementFormDefault="qualified"`) {
		t.Errorf("a namespaced document has qualified elements:\n%s", xsd)
	}
	if !strings.Contains(xsd, `type="tns:AType"`) {
		t.Errorf("type references should be prefixed:\n%s", xsd)
	}
}

func TestTextWithChildrenIsMixed(t *testing.T) {
	s := New(DefaultOptions())
	if err := s.Add(strings.NewReader(`<r>lead <b>bold</b> tail</r>`)); err != nil {
		t.Fatal(err)
	}
	xsd := s.XSD()
	if !strings.Contains(xsd, `mixed="true"`) {
		t.Errorf("want mixed content:\n%s", xsd)
	}
	if len(s.Warnings) == 0 {
		t.Error("mixed content should be reported as an approximation")
	}
}

func TestSimpleContentForTextWithAttributes(t *testing.T) {
	xsd := build(t, DefaultOptions(), `<r><price currency="USD">9.99</price></r>`)
	if !strings.Contains(xsd, `<xs:extension base="xs:decimal">`) {
		t.Errorf("want simple content:\n%s", xsd)
	}
}

func TestInferredSchemaIsWellFormed(t *testing.T) {
	// The output is XML before it is a schema, and an unescaped value in a
	// sample would break it.
	xsd := build(t, DefaultOptions(),
		`<r><v note="a &amp; b">&lt;tagged&gt;</v></r>`)
	if err := parseXML(xsd); err != nil {
		t.Fatalf("output is not well-formed XML: %v\n%s", err, xsd)
	}
}

// parseXML reports whether a document is well-formed.
func parseXML(doc string) error {
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
