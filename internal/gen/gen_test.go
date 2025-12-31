package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chriswirz/xsd2code-go/internal/ir"
	"github.com/chriswirz/xsd2code-go/internal/xsd"
)

// schemaPath is the fixture every generator test runs against. It exercises
// inheritance, a choice, an enumeration, a group, an attribute group, repeated
// content, an xs:list and simple content.
const schemaPath = "../../testdata/purchaseorder.xsd"

func model(t *testing.T) *ir.Model {
	t.Helper()
	set, err := xsd.Load(schemaPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return ir.Build(set)
}

// generate runs one generator and returns the files by name.
func generate(t *testing.T, o Options) map[string]string {
	t.Helper()
	files, err := Generate(model(t), o)
	if err != nil {
		t.Fatalf("generate %s: %v", o.Language, err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Name] = string(f.Content)
	}
	return out
}

func TestGoOutput(t *testing.T) {
	files := generate(t, Options{Language: "go", Package: "po", Postgres: true})
	src, ok := files["models.go"]
	if !ok {
		t.Fatalf("no models.go in %v", keys(files))
	}
	for _, want := range []string{
		// The root element pins the struct to its element name and gets a
		// helper of its own.
		"func UnmarshalPurchaseOrder(data []byte) (*PurchaseOrderType, error)",
		"XMLName xml.Name `xml:\"http://example.com/po purchaseOrder\"`",
		// Extension is embedding, so the base fields marshal inline.
		"type USAddress struct {\n\t// AddressBase is the base type",
		// An optional scalar is a pointer: absent and zero are different.
		"Comment *string",
		// A repeated element is a slice and never a pointer.
		"Street []string",
		// The enumeration is a named string type with its values.
		`OrderStatusOnHold    OrderStatus = "on-hold"`,
		// Persistence tags ride alongside the XML ones.
		`db:"order_date"`,
		// Repeated complex content has no column of its own.
		`db:"-"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("models.go is missing %q", want)
		}
	}
	if _, ok := files["xsdtypes.go"]; !ok {
		t.Error("the support file should accompany the models")
	}
	if _, ok := files["schema.sql"]; !ok {
		t.Error("schema.sql should be emitted when persistence is on")
	}
}

func TestPostgresCanBeTurnedOff(t *testing.T) {
	files := generate(t, Options{Language: "go", Package: "po"})
	if _, ok := files["schema.sql"]; ok {
		t.Error("no DDL should be emitted without --postgres")
	}
	if strings.Contains(files["models.go"], `db:"`) {
		t.Error("no db tags should be emitted without --postgres")
	}
}

func TestCSharpOutput(t *testing.T) {
	files := generate(t, Options{Language: "csharp", Package: "Contoso.Orders", Postgres: true})
	src := files["Models.cs"]
	for _, want := range []string{
		"namespace Contoso.Orders",
		`[XmlType(TypeName = "PurchaseOrderType", Namespace = "http://example.com/po")]`,
		`[XmlRoot(ElementName = "purchaseOrder", Namespace = "http://example.com/po")]`,
		"public class USAddress : AddressBase",
		"public abstract class AddressBase",
		// XmlSerializer refuses Nullable<T> on an attribute, for every T, so an
		// optional one carries its absence in the Specified companion instead.
		"public OrderStatus Status { get; set; }",
		"public bool StatusSpecified { get; set; }",
		// An optional element may still be nullable, which is clearer.
		"public DateTime? ShipDate { get; set; }",
		// An xs:list is one value on the wire and a collection in the object.
		"public List<string> Tags { get; set; } = new List<string>();",
		"public string TagsSerialized",
		"public static class XsdList",
		`[XmlText]`,
		`[XmlEnum(Name = "on-hold")]`,
		`[Table("purchase_order_type")]`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("Models.cs is missing %q", want)
		}
	}
	ctx, ok := files["OrdersDbContext.cs"]
	if !ok {
		t.Fatalf("no DbContext in %v", keys(files))
	}
	for _, want := range []string{
		"public class OrdersDbContext : DbContext",
		"entity.UseTptMappingStrategy();",
		"UsingEntity(j => j.ToTable(",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("the DbContext is missing %q", want)
		}
	}
}

// TestCSharpDocumentMethods pins the entry points a caller reaches for: a
// document as text, and a document on disk. They are the same in both C#
// formats, which is what makes the format a regeneration rather than a rewrite
// of the calling code.
func TestCSharpDocumentMethods(t *testing.T) {
	for _, xmlSerializable := range []bool{false, true} {
		files := generate(t, Options{Language: "csharp", Package: "Contoso.Orders",
			XmlSerializable: xmlSerializable})
		src := files["Models.cs"]
		for _, want := range []string{
			"public static string ToXmlPurchaseOrder(PurchaseOrderType value)",
			"public static void SavePurchaseOrder(string path, PurchaseOrderType value)",
			"public static void WritePurchaseOrder(Stream stream, PurchaseOrderType value)",
			"public static void WritePurchaseOrder(XmlWriter writer, PurchaseOrderType value)",
			"public static PurchaseOrderType LoadPurchaseOrder(string path)",
			"public static PurchaseOrderType ParsePurchaseOrder(string xml)",
			// StringWriter reports UTF-16, which would contradict the
			// declaration on the returned text.
			"private sealed class Utf8StringWriter : StringWriter",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("ixmlserializable=%v: Models.cs is missing %q", xmlSerializable, want)
			}
		}
	}
}

func TestCSharpXmlSerializableOutput(t *testing.T) {
	files := generate(t, Options{Language: "csharp", Package: "Contoso.Orders",
		Postgres: true, XmlSerializable: true})
	src := files["Models.cs"]
	for _, want := range []string{
		// The top of a hierarchy implements the interface; the types below it
		// override the virtuals it calls.
		"public class PurchaseOrderType : IXmlSerializable",
		"public abstract class AddressBase : IXmlSerializable",
		"public class USAddress : AddressBase",
		"public XmlSchema GetSchema()",
		"public void ReadXml(XmlReader reader)",
		"public void WriteXml(XmlWriter writer)",
		"protected virtual bool ReadXmlElement(XmlReader reader)",
		"protected override bool ReadXmlElement(XmlReader reader)",
		"return base.ReadXmlElement(reader);",
		// Values are converted by name, not by looking an attribute up.
		"public static OrderStatus Parse(string text)",
		`case "on-hold":`,
		// A base-typed member reads the derivation the document names.
		"public static AddressBase Create(XmlReader reader)",
		"XsdXml.ReadTypeName(reader)",
		// An xs:list is split and joined in generated code.
		"foreach (var item in XsdXml.SplitList(text))",
		// The persistence mapping is untouched by the format.
		`[Table("purchase_order_type")]`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("Models.cs is missing %q", want)
		}
	}
	// Nothing on this path may reach for reflection, which is the whole point
	// of the format.
	for _, unwanted := range []string{
		"XmlSerializer(",
		"[XmlElement(",
		"[XmlAttribute(",
		"[XmlRoot(",
		"[XmlEnum(",
		"[XmlIgnore]",
		"System.Reflection",
		"GetCustomAttribute",
		"TagsSerialized",
	} {
		if strings.Contains(src, unwanted) {
			t.Errorf("Models.cs should not contain %q", unwanted)
		}
	}
	if _, ok := files["OrdersDbContext.cs"]; !ok {
		t.Errorf("the DbContext should still be emitted in %v", keys(files))
	}
}

func TestJavaOutput(t *testing.T) {
	files := generate(t, Options{Language: "java", Package: "com.contoso.orders", Postgres: true})
	// Java needs one public type per file, in the package's directory.
	src, ok := files["com/contoso/orders/USAddress.java"]
	if !ok {
		t.Fatalf("no USAddress.java in %v", keys(files))
	}
	for _, want := range []string{
		"package com.contoso.orders;",
		"public class USAddress extends AddressBase {",
		`@XmlType(name = "USAddress", namespace = "http://example.com/po")`,
		"public String getCity() {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("USAddress.java is missing %q", want)
		}
	}

	items := files["com/contoso/orders/Items.java"]
	for _, want := range []string{
		"@OneToMany(cascade = CascadeType.ALL",
		"@JoinTable(name = \"items_item_link\"",
		// Document order is data, so the link table keeps an ordinal.
		`@OrderColumn(name = "ordinal")`,
	} {
		if !strings.Contains(items, want) {
			t.Errorf("Items.java is missing %q", want)
		}
	}

	status := files["com/contoso/orders/OrderStatus.java"]
	for _, want := range []string{
		`@XmlEnumValue("on-hold")`,
		"ON_HOLD(\"on-hold\");",
		// The database has to store the lexical value, not the constant name.
		"implements AttributeConverter<OrderStatus, String>",
	} {
		if !strings.Contains(status, want) {
			t.Errorf("OrderStatus.java is missing %q", want)
		}
	}

	if _, ok := files["com/contoso/orders/XmlDocuments.java"]; !ok {
		t.Error("the JAXB entry points should be generated")
	}
}

func TestDDLOutput(t *testing.T) {
	files := generate(t, Options{Language: "go", Package: "po", Postgres: true})
	ddl := files["schema.sql"]
	for _, want := range []string{
		`CREATE TABLE IF NOT EXISTS "purchase_order_type" (`,
		`"id" bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY`,
		// A derived table's key is also the foreign key onto its base.
		`CREATE TABLE IF NOT EXISTS "us_address" (`,
		`"id" bigint PRIMARY KEY`,
		// An enumeration is a text column held by a CHECK, not a Postgres enum
		// type that no two ORMs map alike.
		`CHECK ("status" IN ('pending', 'shipped', 'cancelled', 'on-hold'))`,
		// A repeated primitive is an array rather than a table of its own.
		`"street" text[]`,
		`CREATE TABLE IF NOT EXISTS "items_item" (`,
		"ON DELETE CASCADE",
		"COMMENT ON COLUMN",
		"BEGIN;",
		"COMMIT;",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("schema.sql is missing %q", want)
		}
	}
}

func TestTablePrefix(t *testing.T) {
	files := generate(t, Options{Language: "go", Postgres: true, TablePrefix: "po_"})
	if !strings.Contains(files["schema.sql"], `"po_purchase_order_type"`) {
		t.Error("the table prefix was not applied")
	}
}

func TestUnknownLanguage(t *testing.T) {
	if _, err := Generate(model(t), Options{Language: "cobol"}); err == nil {
		t.Error("an unsupported language should be an error")
	}
}

// TestGeneratedGoRoundTrips is the test that matters: it writes the generated
// package to a temporary module, compiles it, and unmarshals a real document
// with it. Everything else here checks that the output looks right; this
// checks that it works.
func TestGeneratedGoRoundTrips(t *testing.T) {
	// Skipping is a convenience for a local run, never for CI: a skip reads
	// exactly like a pass in the summary, and this is the test that proves the
	// generated code works rather than merely looks right.
	ci := os.Getenv("CI") != ""
	if testing.Short() && !ci {
		t.Skip("compiling the generated package takes a few seconds")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		if ci {
			t.Fatalf("no Go toolchain on PATH: %v", err)
		}
		t.Skip("no Go toolchain available")
	}

	dir := t.TempDir()
	files := generate(t, Options{Language: "go", Package: "po", Postgres: true})
	for name, content := range files {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sample, err := os.ReadFile("../../testdata/purchaseorder.xml")
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "go.mod", "module po\n\ngo 1.25\n")
	write(t, dir, "sample.xml", string(sample))
	write(t, dir, "roundtrip_test.go", roundTripTest)

	cmd := exec.Command(goTool, "test", "./...")
	cmd.Dir = dir
	// A sandboxed build must not reach for the network or a shared cache it
	// cannot write.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the generated package failed to build or pass:\n%s", out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// roundTripTest is compiled against the generated package. It asserts on the
// things a caller actually depends on: that the document parses, that every
// kind of member arrives with the right value, and that writing it back
// produces the same shape.
const roundTripTest = `package po

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestUnmarshalSample(t *testing.T) {
	order, err := LoadPurchaseOrder("sample.xml")
	if err != nil {
		t.Fatal(err)
	}

	// An attribute bound through a generated adapter type.
	if got := order.OrderDate.String(); got != "1999-10-20" {
		t.Errorf("orderDate = %q", got)
	}
	// An optional attribute with an enumerated type.
	if order.Status == nil || *order.Status != OrderStatusShipped {
		t.Errorf("status = %v", order.Status)
	}
	if !order.Status.Valid() {
		t.Error("shipped should be a declared value")
	}
	// Inherited content: name and street come from the embedded base type.
	if order.ShipTo == nil || order.ShipTo.Name != "Alice Smith" {
		t.Fatalf("shipTo = %+v", order.ShipTo)
	}
	if len(order.ShipTo.Street) != 2 || order.ShipTo.Street[1] != "Apartment 4" {
		t.Errorf("street = %v", order.ShipTo.Street)
	}
	// Repeated complex content.
	if order.Items == nil || len(order.Items.Item) != 2 {
		t.Fatalf("items = %+v", order.Items)
	}
	first := order.Items.Item[0]
	if first.PartNum != "872-AA" || first.ProductName != "Lawnmower" || first.Quantity != 1 {
		t.Errorf("first item = %+v", first)
	}
	// Simple content: a value with an attribute on it.
	if first.Price == nil || first.Price.Value != "148.95" || first.Price.Currency != "USD" {
		t.Errorf("price = %+v", first.Price)
	}
	// An xs:list arrives as a slice of its item type.
	if len(first.Tags) != 3 || first.Tags[0] != "garden" {
		t.Errorf("tags = %v", first.Tags)
	}
	// An absent optional element stays nil rather than becoming a zero value.
	if first.ShipDate != nil {
		t.Errorf("shipDate should be absent, got %v", first.ShipDate)
	}
	if second := order.Items.Item[1]; second.ShipDate == nil || second.ShipDate.String() != "1999-05-21" {
		t.Errorf("second shipDate = %v", second.ShipDate)
	}
	// Only one arm of the choice is populated.
	if order.CreditCard == nil || order.Invoice != nil {
		t.Errorf("choice = %+v / %+v", order.CreditCard, order.Invoice)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	order, err := LoadPurchaseOrder("sample.xml")
	if err != nil {
		t.Fatal(err)
	}
	out, err := xml.Marshal(order)
	if err != nil {
		t.Fatal(err)
	}
	again, err := UnmarshalPurchaseOrder(out)
	if err != nil {
		t.Fatalf("re-parsing our own output failed: %v\n%s", err, out)
	}
	if again.ShipTo.City != order.ShipTo.City || len(again.Items.Item) != len(order.Items.Item) {
		t.Errorf("round trip lost content:\n%s", out)
	}
	if !strings.Contains(string(out), "purchaseOrder") {
		t.Errorf("the root element name was not preserved:\n%s", out)
	}
}
`

func TestTypeScriptOutput(t *testing.T) {
	files := generate(t, Options{Language: "typescript", Postgres: true})
	src, ok := files["models.ts"]
	if !ok {
		t.Fatalf("no models.ts in %v", keys(files))
	}
	for _, want := range []string{
		`export const NAMESPACE: string | null = "http://example.com/po";`,
		"export interface USAddress extends AddressBase {",
		// An enumeration is a union of the lexical values, which is what the
		// document actually holds.
		`export type OrderStatus = "pending" | "shipped" | "cancelled" | "on-hold";`,
		// An optional member is spelled with ?, not with null.
		"comment?: string;",
		"street: string[];",
		"export function parsePurchaseOrder(xml: string): PurchaseOrderType {",
		"export function readUSAddress(el: Element): USAddress {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("models.ts is missing %q", want)
		}
	}
	// The DDL comes with every language.
	if _, ok := files["schema.sql"]; !ok {
		t.Error("schema.sql should accompany the TypeScript")
	}
}

func TestJavaScriptOutputCarriesNoTypeAnnotations(t *testing.T) {
	files := generate(t, Options{Language: "javascript"})
	src, ok := files["models.js"]
	if !ok {
		t.Fatalf("no models.js in %v", keys(files))
	}
	// The JavaScript is the TypeScript with its annotations removed by
	// substitution, so a missed one is a syntax error in the shipped file.
	// These are the shapes that would survive such a miss.
	for _, leak := range []string{
		": string", ": Element", ": Document", ": boolean", ": number",
		"as Element", "as any", "declare const", "<T>(", "readonly string[]",
	} {
		if strings.Contains(src, leak) {
			t.Errorf("a TypeScript annotation reached the JavaScript: %q", leak)
		}
	}
	if !strings.Contains(src, "export function parsePurchaseOrder(xml) {") {
		t.Error("the JavaScript entry point is missing")
	}
	// The declarations still carry the types.
	decls := files["models.d.ts"]
	if !strings.Contains(decls, "export declare function parsePurchaseOrder(xml: string): PurchaseOrderType;") {
		t.Errorf("models.d.ts is missing the typed entry point:\n%s", decls)
	}
}

func TestPythonOutput(t *testing.T) {
	files := generate(t, Options{Language: "python", Postgres: true})
	src, ok := files["models.py"]
	if !ok {
		t.Fatalf("no models.py in %v", keys(files))
	}
	for _, want := range []string{
		"class OrderStatus(str, Enum):",
		`ON_HOLD = "on-hold"`,
		"class USAddress(AddressBase):",
		"def read_us_address(el: ET.Element) -> USAddress:",
		"def load_purchase_order(path: str) -> PurchaseOrderType:",
		// Every field is defaulted, or a dataclass with an inherited default
		// would not compile.
		"street: list[str] = field(default_factory=list)",
		`TABLE = "us_address"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("models.py is missing %q", want)
		}
	}
	// A base class has to be defined before the class statement that names it.
	if strings.Index(src, "class AddressBase") > strings.Index(src, "class USAddress(AddressBase)") {
		t.Error("USAddress is declared before the base it extends")
	}
}

func TestRustOutput(t *testing.T) {
	files := generate(t, Options{Language: "rust", Postgres: true})
	src, ok := files["models.rs"]
	if !ok {
		t.Fatalf("no models.rs in %v", keys(files))
	}
	for _, want := range []string{
		"pub struct PurchaseOrderType {",
		// quick-xml spells an attribute with a leading @ and text with $text.
		`#[serde(rename = "@orderDate")]`,
		`#[serde(rename = "$text")]`,
		`#[serde(rename = "on-hold")]`,
		"pub fn parse_purchase_order(xml: &str) -> Result<PurchaseOrderType, quick_xml::DeError> {",
		// An xs:list needs a helper: serde sees one string.
		`#[serde(with = "xsd_list"`,
		"pub mod xsd_list {",
		`#[cfg_attr(feature = "sqlx", derive(sqlx::FromRow))]`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("models.rs is missing %q", want)
		}
	}
	// quick-xml does not support serde's flatten, so an extension's inherited
	// members are written into the derived struct instead.
	if strings.Contains(src, "serde(flatten)") {
		t.Error("flatten is not supported by quick-xml and must not be emitted")
	}
	if !strings.Contains(src, "pub struct USAddress {") || !strings.Contains(src, "pub street: Vec<String>,") {
		t.Error("USAddress should carry the inherited members itself")
	}
}

func TestCppOutput(t *testing.T) {
	files := generate(t, Options{Language: "cpp", Package: "demo.orders", Postgres: true})
	hpp, ok := files["models.hpp"]
	if !ok {
		t.Fatalf("no models.hpp in %v", keys(files))
	}
	for _, want := range []string{
		"namespace demo::orders {",
		"struct USAddress : AddressBase {",
		"enum class OrderStatus {",
		"PurchaseOrderType load_purchase_order(const std::string& path);",
		"std::optional<",
		"std::vector<",
	} {
		if !strings.Contains(hpp, want) {
			t.Errorf("models.hpp is missing %q", want)
		}
	}
	// A type must be complete before it is held by value, so a base has to be
	// declared before what extends it.
	if strings.Index(hpp, "struct AddressBase {") > strings.Index(hpp, "struct USAddress : AddressBase {") {
		t.Error("USAddress is defined before its base")
	}
	cpp := files["models.cpp"]
	for _, want := range []string{
		`#include "models.hpp"`,
		"USAddress read_us_address(const pugi::xml_node& node) {",
		"OrderStatus parse_order_status(const std::string& value) {",
	} {
		if !strings.Contains(cpp, want) {
			t.Errorf("models.cpp is missing %q", want)
		}
	}
}

func TestEveryLanguageGenerates(t *testing.T) {
	// A language listed in the help has to produce something, whatever else is
	// true of it.
	for _, lang := range Languages() {
		t.Run(lang, func(t *testing.T) {
			files := generate(t, Options{Language: lang, Package: "demo.orders", Postgres: true})
			if len(files) < 2 {
				t.Fatalf("%s produced %v", lang, keys(files))
			}
			for name, content := range files {
				if len(strings.TrimSpace(content)) == 0 {
					t.Errorf("%s produced an empty %s", lang, name)
				}
			}
		})
	}
}

func TestKotlinOutput(t *testing.T) {
	files := generate(t, Options{Language: "kotlin", Package: "acme.orders", Postgres: true})
	src, ok := files["acme/orders/Models.kt"]
	if !ok {
		t.Fatalf("no Models.kt in %v", keys(files))
	}
	for _, want := range []string{
		"package acme.orders",
		"enum class OrderStatus(val value: String) {",
		`ON_HOLD("on-hold");`,
		"data class USAddress(",
		"fun readUSAddress(el: DomElement): USAddress = USAddress(",
		// The DOM imports are aliased: an explicit import beats a
		// same-package declaration in Kotlin, so a schema type called Node,
		// Element or Document would otherwise lose to org.w3c.dom.
		"import org.w3c.dom.Node as DomNode",
		"fun loadPurchaseOrder(file: File): PurchaseOrderType",
		// A data class cannot extend one, so the inherited members are here.
		"val name: String",
		"val street: List<String> = emptyList()",
		// An empty map cannot be inferred, so the type is written out.
		"val COLUMNS: Map<String, String> = mapOf(",
		`const val TABLE = "us_address"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("Models.kt is missing %q", want)
		}
	}
	// No JAXB: the point of the Kotlin output is that it needs only the JDK.
	if strings.Contains(src, "jakarta.") || strings.Contains(src, "javax.xml.bind") {
		t.Error("the Kotlin output should depend on nothing beyond the JDK")
	}
}

func TestSwiftOutput(t *testing.T) {
	files := generate(t, Options{Language: "swift", Postgres: true})
	src, ok := files["Models.swift"]
	if !ok {
		t.Fatalf("no Models.swift in %v", keys(files))
	}
	for _, want := range []string{
		"import Foundation",
		"public enum OrderStatus: String, Codable, CaseIterable {",
		`case onHold = "on-hold"`,
		"public struct USAddress {",
		// XMLParser is in FoundationXML on Linux and in Foundation on Apple
		// platforms; canImport is the only spelling that satisfies both.
		"#if canImport(FoundationXML)",
		"public func readUSAddress(_ el: XSDElement) throws -> USAddress {",
		"public func loadPurchaseOrder(contentsOf url: URL) throws -> PurchaseOrderType {",
		// The tree is built from XMLParser, which exists on every platform,
		// unlike XMLDocument. It is not called XMLNode: Foundation has one of
		// those on Apple platforms.
		"public final class XSDElement",
		"XMLParser(data: data)",
		"public static let table = \"us_address\"",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("Models.swift is missing %q", want)
		}
	}
	// @retroactive does not parse before Swift 6, so the conformance it would
	// have silenced must not be there at all.
	if strings.Contains(src, "@retroactive") {
		t.Error("@retroactive does not parse on Swift 5")
	}
	if strings.Contains(src, "XMLDocument") {
		t.Error("XMLDocument is not available on every platform Swift runs on")
	}
	// try belongs where something throws: doubled it does not compile, and
	// spurious it is a warning. Both shipped once.
	if strings.Contains(src, "try try") {
		t.Error("a doubled try does not compile")
	}
	if strings.Contains(src, "{ try $0.text }") {
		t.Error("$0.text does not throw, so try over it is a warning")
	}
	// An empty dictionary literal is [:] in Swift. Items has no columns of its
	// own -- its only member is repeated complex content -- so the fixture
	// exercises this every time.
	if !strings.Contains(src, "public static let columns: [String: String] = [:]") {
		t.Error("an empty columns dictionary has to be spelled [:]")
	}
}

func TestSelfReferentialTypesAreHeldByReference(t *testing.T) {
	// A value that contains itself has no size, in Rust or in Swift. The
	// fixture has no recursion, so the analysis is exercised directly.
	set, err := xsd.Load("../../testdata/recursive.xsd")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m := ir.Build(set)

	cycles := selfReferential(m)
	if !cycles["Node"] {
		t.Fatalf("Node reaches itself by value; cycles = %v", cycles)
	}

	rust, err := Generate(m, Options{Language: "rust"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rust[0].Content), "Option<Box<Node>>") {
		t.Errorf("the recursive member needs a Box:\n%s", rust[0].Content)
	}

	swift, err := Generate(m, Options{Language: "swift"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(swift[0].Content), "public final class Node {") {
		t.Errorf("a recursive type has to be a reference type:\n%s", swift[0].Content)
	}
}
