package xsdout

import (
	"strings"
	"testing"

	"github.com/chriswirz/xsd2code-go/internal/ir"
	"github.com/chriswirz/xsd2code-go/internal/xsd"
)

// build parses a schema and resolves it, which is the input this package takes.
func build(t *testing.T, path string) *ir.Model {
	t.Helper()
	set, err := xsd.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return ir.Build(set)
}

// reparse writes a model out and reads it back, which is the property that
// matters: what this package emits has to be a schema the parser accepts.
func reparse(t *testing.T, m *ir.Model) *ir.Model {
	t.Helper()
	doc := Write(m, Options{})
	schema, err := xsd.Parse([]byte(doc), "generated.xsd")
	if err != nil {
		t.Fatalf("the emitted schema does not parse: %v\n%s", err, doc)
	}
	set := &xsd.Set{Roots: []*xsd.Schema{schema}, All: []*xsd.Schema{schema}}
	return ir.Build(set)
}

func TestRoundTripPreservesTheModel(t *testing.T) {
	original := build(t, "../../testdata/purchaseorder.xsd")
	again := reparse(t, original)

	if len(again.Roots) != len(original.Roots) {
		t.Fatalf("roots = %d, want %d", len(again.Roots), len(original.Roots))
	}
	if again.Roots[0].XMLName != "purchaseOrder" {
		t.Errorf("root = %q", again.Roots[0].XMLName)
	}
	// Every named type has to survive. Anonymous types are named after the
	// element that owns them, and a round trip turns them into named types, so
	// the count may grow; nothing may vanish.
	for _, want := range original.Types {
		if again.Lookup(want.Name) == nil {
			t.Errorf("type %s did not survive the round trip", want.Name)
		}
	}
}

func TestRoundTripPreservesFieldShape(t *testing.T) {
	original := build(t, "../../testdata/purchaseorder.xsd")
	again := reparse(t, original)

	// The properties a generator depends on: where a value lives, whether it
	// may be absent, and whether there may be several.
	for _, want := range original.Types {
		if want.Kind != ir.Class {
			continue
		}
		got := again.Lookup(want.Name)
		if got == nil {
			continue
		}
		if got.Base != want.Base {
			t.Errorf("%s: base = %q, want %q", want.Name, got.Base, want.Base)
		}
		if got.Abstract != want.Abstract {
			t.Errorf("%s: abstract = %v, want %v", want.Name, got.Abstract, want.Abstract)
		}
		if len(got.Fields) != len(want.Fields) {
			t.Errorf("%s: %d fields, want %d", want.Name, len(got.Fields), len(want.Fields))
			continue
		}
		for i, wf := range want.Fields {
			gf := got.Fields[i]
			if gf.XMLName != wf.XMLName || gf.Origin != wf.Origin {
				t.Errorf("%s.%s: got %q/%v, want %q/%v",
					want.Name, wf.Name, gf.XMLName, gf.Origin, wf.XMLName, wf.Origin)
			}
			if gf.Repeated != wf.Repeated {
				t.Errorf("%s.%s: repeated = %v, want %v", want.Name, wf.Name, gf.Repeated, wf.Repeated)
			}
			// A required field must not come back optional: that would turn a
			// schema violation into something the generated code accepts.
			if !wf.Optional && gf.Optional && !wf.Repeated {
				t.Errorf("%s.%s: a required field came back optional", want.Name, wf.Name)
			}
			if gf.List != wf.List {
				t.Errorf("%s.%s: list = %v, want %v", want.Name, wf.Name, gf.List, wf.List)
			}
		}
	}
}

func TestEnumsSurvive(t *testing.T) {
	original := build(t, "../../testdata/purchaseorder.xsd")
	again := reparse(t, original)

	status := again.Lookup("OrderStatus")
	if status == nil || status.Kind != ir.Enum {
		t.Fatalf("OrderStatus = %+v", status)
	}
	if len(status.Values) != 4 || status.Values[3].Value != "on-hold" {
		t.Errorf("values = %+v", status.Values)
	}
}

func TestNamespaceAndQualification(t *testing.T) {
	m := build(t, "../../testdata/purchaseorder.xsd")
	doc := Write(m, Options{})

	for _, want := range []string{
		`targetNamespace="http://example.com/po"`,
		`elementFormDefault="qualified"`,
		`type="tns:USAddress"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q from:\n%s", want, doc)
		}
	}
}

func TestOptionsOverrideTheNamespace(t *testing.T) {
	m := build(t, "../../testdata/purchaseorder.xsd")
	doc := Write(m, Options{TargetNamespace: "urn:other", Header: []string{"a note"}})
	if !strings.Contains(doc, `targetNamespace="urn:other"`) {
		t.Errorf("the namespace was not overridden:\n%s", doc)
	}
	if !strings.Contains(doc, "a note") {
		t.Error("the header comment was not written")
	}
}

func TestDocumentationIsEscaped(t *testing.T) {
	// Schema documentation is arbitrary text, and the output is XML first.
	m := ir.NewModel("")
	m.AddType(&ir.Type{
		Name: "T",
		Kind: ir.Class,
		Doc:  `a < b & "c"`,
		Fields: []*ir.Field{{
			Name: "V", XMLName: "v", Origin: ir.ElementField, Builtin: ir.String,
		}},
	})
	doc := Write(m, Options{})
	if strings.Contains(doc, "a < b") {
		t.Errorf("the documentation was not escaped:\n%s", doc)
	}
	if _, err := xsd.Parse([]byte(doc), "t.xsd"); err != nil {
		t.Errorf("the output is not well-formed: %v\n%s", err, doc)
	}
}

func TestSimpleContentIsWrittenAsSimpleContent(t *testing.T) {
	m := build(t, "../../testdata/purchaseorder.xsd")
	doc := Write(m, Options{})
	if !strings.Contains(doc, `<xs:simpleContent>`) {
		t.Errorf("Money should round-trip as simple content:\n%s", doc)
	}
	again := reparse(t, m)
	money := again.Lookup("Money")
	if money == nil || len(money.Fields) != 2 || money.Fields[0].Origin != ir.TextField {
		t.Errorf("Money = %+v", money)
	}
}
