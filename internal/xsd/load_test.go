package xsd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCapturesPrefixes(t *testing.T) {
	// encoding/xml resolves element names but discards the prefix table, and a
	// QName-valued attribute such as type="tns:Foo" needs it.
	doc := []byte(`<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"
           xmlns:tns="urn:test" targetNamespace="urn:test">
  <xs:complexType name="T"/>
</xs:schema>`)

	s, err := Parse(doc, "test.xsd")
	if err != nil {
		t.Fatal(err)
	}
	if s.Prefixes["tns"] != "urn:test" {
		t.Errorf("prefixes = %v", s.Prefixes)
	}
	if s.TargetNamespace != "urn:test" || len(s.ComplexTypes) != 1 {
		t.Errorf("schema = %+v", s)
	}
}

func TestParseRejectsNonSchema(t *testing.T) {
	if _, err := Parse([]byte(`<html/>`), "x.html"); err == nil {
		t.Error("a non-schema document should be rejected")
	}
}

func TestLoadFollowsIncludes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.xsd", `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"
           xmlns:tns="urn:test" targetNamespace="urn:test">
  <xs:include schemaLocation="common.xsd"/>
  <xs:element name="doc" type="tns:Common"/>
</xs:schema>`)
	write(t, dir, "common.xsd", `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:test">
  <xs:complexType name="Common"/>
</xs:schema>`)

	set, err := Load(filepath.Join(dir, "main.xsd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.All) != 2 {
		t.Fatalf("loaded %d documents, want 2", len(set.All))
	}
	if len(set.Roots) != 1 {
		t.Errorf("roots = %d, want the one document named", len(set.Roots))
	}
}

func TestLoadSurvivesACircularInclude(t *testing.T) {
	// Two schemas including each other is legal and not especially rare.
	dir := t.TempDir()
	write(t, dir, "a.xsd", `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:test">
  <xs:include schemaLocation="b.xsd"/>
</xs:schema>`)
	write(t, dir, "b.xsd", `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:test">
  <xs:include schemaLocation="a.xsd"/>
</xs:schema>`)

	set, err := Load(filepath.Join(dir, "a.xsd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.All) != 2 {
		t.Errorf("loaded %d documents, want 2", len(set.All))
	}
}

func TestMissingImportIsRecordedNotFatal(t *testing.T) {
	// Importing a namespace whose schema is not shipped -- xlink, xhtml, an
	// internal registry -- is ordinary, and it must not stop generation.
	dir := t.TempDir()
	write(t, dir, "main.xsd", `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:test">
  <xs:import namespace="urn:other" schemaLocation="other.xsd"/>
  <xs:import namespace="urn:remote" schemaLocation="https://example.com/remote.xsd"/>
</xs:schema>`)

	set, err := Load(filepath.Join(dir, "main.xsd"))
	if err != nil {
		t.Fatalf("a missing import should not be fatal: %v", err)
	}
	if len(set.Missing) != 2 {
		t.Errorf("missing = %v, want the local miss and the network one", set.Missing)
	}
}

func TestParticleOrderIsPreserved(t *testing.T) {
	// A group reference between two elements contributes its content at that
	// point, which is only expressible if the members stay in document order.
	doc := []byte(`<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:test">
  <xs:complexType name="T">
    <xs:sequence>
      <xs:element name="a"/>
      <xs:group ref="g"/>
      <xs:choice><xs:element name="b"/></xs:choice>
      <xs:any/>
    </xs:sequence>
  </xs:complexType>
</xs:schema>`)

	s, err := Parse(doc, "test.xsd")
	if err != nil {
		t.Fatal(err)
	}
	items := s.ComplexTypes[0].Sequence.Items
	want := []ParticleKind{ElementParticle, GroupParticle, ChoiceParticle, AnyParticle}
	if len(items) != len(want) {
		t.Fatalf("got %d particles, want %d", len(items), len(want))
	}
	for i, kind := range want {
		if items[i].Kind != kind {
			t.Errorf("particle %d is kind %d, want %d", i, items[i].Kind, kind)
		}
	}
}

func TestAnnotationDocCollapsesWhitespace(t *testing.T) {
	doc := []byte(`<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:complexType name="T">
    <xs:annotation>
      <xs:documentation>
        A type
        described over two lines.
      </xs:documentation>
    </xs:annotation>
  </xs:complexType>
</xs:schema>`)

	s, err := Parse(doc, "test.xsd")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ComplexTypes[0].Annotation.Doc(); got != "A type described over two lines." {
		t.Errorf("Doc() = %q", got)
	}
	var nilAnnotation *Annotation
	if got := nilAnnotation.Doc(); got != "" {
		t.Errorf("a missing annotation should document nothing, got %q", got)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
