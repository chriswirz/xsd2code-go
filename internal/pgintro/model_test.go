package pgintro

import (
	"testing"

	"github.com/chriswirz/xsd2code-go/internal/ir"
)

func TestParseCheckEnum(t *testing.T) {
	cases := []struct {
		name   string
		def    string
		column string
		values []string
	}{
		{
			// The form Postgres stores CHECK (status IN ('a','b')) as.
			name:   "normalized IN list",
			def:    `CHECK (((status)::text = ANY ((ARRAY['open'::character varying, 'closed'::character varying])::text[])))`,
			column: "status",
			values: []string{"open", "closed"},
		},
		{
			name:   "quoted column",
			def:    `CHECK (("status" = ANY (ARRAY['open'::text, 'on-hold'::text])))`,
			column: "status",
			values: []string{"open", "on-hold"},
		},
		{
			name:   "an escaped quote in a value",
			def:    `CHECK ((kind = ANY (ARRAY['it''s'::text])))`,
			column: "kind",
			values: []string{"it's"},
		},
		// Only a closed set of values is an enumeration; everything else is a
		// constraint the schema has no way to express.
		{name: "a range check", def: `CHECK ((qty > 0))`},
		{name: "a length check", def: `CHECK ((length(name) < 10))`},
		{name: "not null-ish", def: `CHECK ((name IS NOT NULL))`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			col, values := parseCheckEnum(c.def)
			if col != c.column {
				t.Fatalf("column = %q, want %q", col, c.column)
			}
			if len(values) != len(c.values) {
				t.Fatalf("values = %v, want %v", values, c.values)
			}
			for i := range values {
				if values[i] != c.values[i] {
					t.Errorf("value %d = %q, want %q", i, values[i], c.values[i])
				}
			}
		})
	}
}

func TestBaseTypeName(t *testing.T) {
	cases := map[string]string{
		"text":                        "text",
		"text[]":                      "text",
		"character varying(20)":       "character varying",
		"numeric(20,0)":               "numeric",
		"timestamp with time zone":    "timestamp with time zone",
		"public.myenum":               "myenum",
		`"WeirdCase"`:                 "WeirdCase",
		"timestamp without time zone": "timestamp without time zone",
	}
	for in, want := range cases {
		if got := baseTypeName(in); got != want {
			t.Errorf("baseTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuiltinFor(t *testing.T) {
	cases := map[string]ir.Builtin{
		"text":                     ir.String,
		"character varying":        ir.String,
		"bigint":                   ir.Long,
		"integer":                  ir.Int,
		"boolean":                  ir.Bool,
		"numeric":                  ir.Decimal,
		"timestamp with time zone": ir.DateTime,
		"date":                     ir.Date,
		"interval":                 ir.Duration,
		"bytea":                    ir.Base64Binary,
		// A type nothing maps: its text form always round-trips, which is more
		// than a guessed narrower mapping can promise.
		"tsvector": ir.String,
	}
	for in, want := range cases {
		if got := builtinFor(in); got != want {
			t.Errorf("builtinFor(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLiteralDefault(t *testing.T) {
	cases := map[string]string{
		"'pending'::text": "pending",
		"'it''s'::text":   "it's",
		"42":              "42",
		"true":            "true",
		"'-1'::integer":   "-1",
		// A function call describes how to make a value, not a value.
		"now()":                    "",
		"nextval('seq'::regclass)": "",
		"gen_random_uuid()":        "",
		"NULL":                     "",
		"":                         "",
	}
	for in, want := range cases {
		if got := literalDefault(in); got != want {
			t.Errorf("literalDefault(%q) = %q, want %q", in, got, want)
		}
	}
}

// catalog builders, so the mapping can be exercised without a server.

func tbl(name string, cols ...*column) *table {
	return &table{name: name, kind: "r", columns: cols, checks: map[string][]string{}}
}

func col(name, sqlType string, notNull bool) *column {
	return &column{
		name:     name,
		sqlType:  sqlType,
		baseType: baseTypeName(sqlType),
		pgType:   baseTypeName(sqlType),
		notNull:  notNull,
	}
}

func cat(tables ...*table) *catalog {
	c := &catalog{byName: map[string]*table{}, enums: map[string][]string{}}
	for _, t := range tables {
		c.tables = append(c.tables, t)
		c.byName[t.name] = t
	}
	return c
}

func TestSurrogateKeyIsDropped(t *testing.T) {
	orders := tbl("orders", col("id", "bigint", true), col("total", "numeric", true))
	orders.columns[0].generated = true
	orders.pk = []string{"id"}

	m := Model(cat(orders), Options{})
	typ := m.Lookup("Orders")
	if typ == nil {
		t.Fatal("no type for orders")
	}
	// A generated identity is a fact about the storage, not about a document.
	if len(typ.Fields) != 1 || typ.Fields[0].Name != "Total" {
		t.Fatalf("fields = %+v, want just Total", typ.Fields)
	}
	if typ.Key != "" {
		t.Errorf("a dropped surrogate should leave no key nominated, got %q", typ.Key)
	}
}

func TestNaturalKeyIsKept(t *testing.T) {
	orders := tbl("orders", col("id", "text", true), col("total", "numeric", true))
	orders.pk = []string{"id"}

	m := Model(cat(orders), Options{})
	typ := m.Lookup("Orders")
	if len(typ.Fields) != 2 {
		t.Fatalf("fields = %+v, want the key kept", typ.Fields)
	}
	if typ.Key != "Id" {
		t.Errorf("Key = %q, want Id", typ.Key)
	}
	if typ.Table != "orders" {
		t.Errorf("Table = %q, want orders", typ.Table)
	}
	if typ.Fields[0].Column != "id" {
		t.Errorf("the field should carry its real column, got %q", typ.Fields[0].Column)
	}

	// The relational mapping must then use that key rather than inventing one.
	rel := ir.Relations(m, "")
	table := rel.Table("Orders")
	if table.Surrogate {
		t.Error("a type with its own key needs no surrogate")
	}
	if key := table.Key(); key.Name != "id" || key.SQLType != "text" {
		t.Errorf("key column = %+v, want the text id", key)
	}
}

func TestForeignKeyBecomesNestedContent(t *testing.T) {
	customers := tbl("customers", col("id", "bigint", true))
	customers.columns[0].generated = true
	customers.pk = []string{"id"}

	orders := tbl("orders", col("id", "bigint", true), col("customer_id", "bigint", true))
	orders.columns[0].generated = true
	orders.pk = []string{"id"}
	orders.fkeys = []*fkey{{column: "customer_id", refTable: "customers", refCol: "id"}}

	m := Model(cat(customers, orders), Options{})
	typ := m.Lookup("Orders")
	if len(typ.Fields) != 1 {
		t.Fatalf("fields = %+v", typ.Fields)
	}
	f := typ.Fields[0]
	// The _id suffix is storage vocabulary; the element is the thing itself.
	if f.Name != "Customer" || f.XMLName != "customer" || f.TypeName != "Customers" {
		t.Errorf("field = %+v, want a nested Customers", f)
	}
	if f.Column != "customer_id" {
		t.Errorf("the field should still know its column, got %q", f.Column)
	}
	// Only the table nothing points at is a document root.
	if len(m.Roots) != 1 || m.Roots[0].XMLName != "orders" {
		t.Errorf("roots = %+v, want just orders", m.Roots)
	}
}

func TestLinkTableBecomesRepeatedContent(t *testing.T) {
	orders := tbl("orders", col("id", "bigint", true))
	orders.columns[0].generated = true
	orders.pk = []string{"id"}

	items := tbl("items", col("id", "bigint", true), col("sku", "text", true))
	items.columns[0].generated = true
	items.pk = []string{"id"}

	link := tbl("orders_item_link",
		col("orders_id", "bigint", true), col("item_id", "bigint", true), col("ordinal", "integer", true))
	link.fkeys = []*fkey{
		{column: "orders_id", refTable: "orders", refCol: "id"},
		{column: "item_id", refTable: "items", refCol: "id"},
	}

	m := Model(cat(orders, items, link), Options{})
	if m.Lookup("OrdersItemLink") != nil {
		t.Error("a link table should not become a type of its own")
	}
	typ := m.Lookup("Orders")
	if len(typ.Fields) != 1 {
		t.Fatalf("fields = %+v", typ.Fields)
	}
	f := typ.Fields[0]
	if f.Name != "Item" || f.TypeName != "Items" || !f.Repeated {
		t.Errorf("field = %+v, want repeated Items", f)
	}
}

func TestAssociationWithPayloadStaysAType(t *testing.T) {
	// Two foreign keys and a column of its own is not a link table: the extra
	// column is data that would be lost.
	orders := tbl("orders", col("id", "bigint", true))
	orders.pk = []string{"id"}
	orders.columns[0].generated = true
	items := tbl("items", col("id", "bigint", true))
	items.pk = []string{"id"}
	items.columns[0].generated = true

	line := tbl("order_lines",
		col("order_id", "bigint", true), col("item_id", "bigint", true), col("quantity", "integer", true))
	line.fkeys = []*fkey{
		{column: "order_id", refTable: "orders", refCol: "id"},
		{column: "item_id", refTable: "items", refCol: "id"},
	}

	m := Model(cat(orders, items, line), Options{})
	if m.Lookup("OrderLines") == nil {
		t.Error("an association carrying data should keep its type")
	}
}

func TestCheckConstraintBecomesAnEnum(t *testing.T) {
	orders := tbl("orders", col("status", "text", true))
	orders.checks["status"] = []string{"open", "on-hold"}

	m := Model(cat(orders), Options{})
	f := m.Lookup("Orders").Fields[0]
	enum := m.Lookup(f.TypeName)
	if enum == nil || enum.Kind != ir.Enum {
		t.Fatalf("status did not become an enum: %+v", f)
	}
	if len(enum.Values) != 2 || enum.Values[1].Value != "on-hold" || enum.Values[1].Name != "OnHold" {
		t.Errorf("values = %+v", enum.Values)
	}
}

func TestPostgresEnumTypeBecomesAnEnum(t *testing.T) {
	orders := tbl("orders", col("mood", "mood", true))
	c := cat(orders)
	c.enums["mood"] = []string{"sad", "happy"}

	m := Model(c, Options{})
	f := m.Lookup("Orders").Fields[0]
	if enum := m.Lookup(f.TypeName); enum == nil || len(enum.Values) != 2 {
		t.Fatalf("mood did not become an enum: %+v", f)
	}
}

func TestArrayColumnBecomesRepeated(t *testing.T) {
	orders := tbl("orders", col("tags", "text[]", false))
	orders.columns[0].array = true

	m := Model(cat(orders), Options{})
	f := m.Lookup("Orders").Fields[0]
	if !f.Repeated || f.Builtin != ir.String {
		t.Errorf("tags = %+v, want a repeated string", f)
	}
	// A repeated field is never also nullable: an empty collection already
	// says the column was null.
	if f.Optional {
		t.Error("a repeated field should not also be optional")
	}
}

func TestEveryTableIsARootWhenReferencesCycle(t *testing.T) {
	a := tbl("a", col("b_id", "bigint", false))
	b := tbl("b", col("a_id", "bigint", false))
	a.fkeys = []*fkey{{column: "b_id", refTable: "b", refCol: "id"}}
	b.fkeys = []*fkey{{column: "a_id", refTable: "a", refCol: "id"}}

	m := Model(cat(a, b), Options{})
	if len(m.Roots) != 2 {
		t.Errorf("roots = %+v, want both tables", m.Roots)
	}
	if len(m.Warnings) == 0 {
		t.Error("a cycle with no obvious root should be reported")
	}
}

func TestTargetNamespaceIsApplied(t *testing.T) {
	orders := tbl("orders", col("total", "numeric", true))
	m := Model(cat(orders), Options{TargetNamespace: "urn:acme"})
	if m.TargetNamespace != "urn:acme" {
		t.Errorf("namespace = %q", m.TargetNamespace)
	}
	if f := m.Lookup("Orders").Fields[0]; f.Namespace != "urn:acme" {
		t.Errorf("field namespace = %q", f.Namespace)
	}
}

func TestLinkParentIsTheOneInThePrimaryKey(t *testing.T) {
	// The generated link table is keyed on (parent, ordinal). Its name is no
	// help here: "items_item_link" is prefixed by both "items_" and
	// "items_item_", so only the key says which side owns the association.
	items := tbl("items", col("id", "bigint", true))
	items.columns[0].generated = true
	items.pk = []string{"id"}

	itemsItem := tbl("items_item", col("id", "bigint", true), col("sku", "text", true))
	itemsItem.columns[0].generated = true
	itemsItem.pk = []string{"id"}

	link := tbl("items_item_link",
		col("item_id", "bigint", true), col("items_id", "bigint", true), col("ordinal", "integer", true))
	// Declared in the order pg_catalog returns them, which is by column name:
	// "item_id" sorts before "items_id", so the child comes first.
	link.fkeys = []*fkey{
		{column: "item_id", refTable: "items_item", refCol: "id"},
		{column: "items_id", refTable: "items", refCol: "id"},
	}
	link.pk = []string{"items_id", "ordinal"}

	m := Model(cat(items, itemsItem, link), Options{})

	parent := m.Lookup("Items")
	if parent == nil || len(parent.Fields) != 1 {
		t.Fatalf("Items = %+v, want one repeated field", parent)
	}
	if f := parent.Fields[0]; f.TypeName != "ItemsItem" || !f.Repeated {
		t.Errorf("Items field = %+v, want repeated ItemsItem", f)
	}
	// And nothing hangs off the child, which is where the association would
	// land if the two sides were read the wrong way round.
	if child := m.Lookup("ItemsItem"); len(child.Fields) != 1 || child.Fields[0].Name != "Sku" {
		t.Errorf("ItemsItem = %+v, want just its own column", child.Fields)
	}
}

func TestJoinedInheritanceBecomesExtension(t *testing.T) {
	// A table whose single-column primary key is also a foreign key onto
	// another table's key is a joined-table subclass -- the shape the DDL
	// generator emits for an extension.
	base := tbl("address_base", col("id", "bigint", true), col("name", "text", true))
	base.columns[0].generated = true
	base.pk = []string{"id"}

	derived := tbl("us_address", col("id", "bigint", true), col("city", "text", true))
	derived.pk = []string{"id"}
	derived.fkeys = []*fkey{{column: "id", refTable: "address_base", refCol: "id"}}

	m := Model(cat(base, derived), Options{})
	typ := m.Lookup("UsAddress")
	if typ == nil || typ.Base != "AddressBase" {
		t.Fatalf("UsAddress = %+v, want it to extend AddressBase", typ)
	}
	// The key is the link to the base, not content: as a field it would be a
	// nested copy of the row this one is part of.
	if len(typ.Fields) != 1 || typ.Fields[0].Name != "City" {
		t.Errorf("fields = %+v, want just City", typ.Fields)
	}
}

func TestSelfReferenceIsNotInheritance(t *testing.T) {
	// A tree table points its key at itself only if it is a subclass of
	// itself, which nothing is; a parent_id pointing home is ordinary.
	node := tbl("node", col("id", "bigint", true), col("parent_id", "bigint", false))
	node.columns[0].generated = true
	node.pk = []string{"id"}
	node.fkeys = []*fkey{{column: "parent_id", refTable: "node", refCol: "id"}}

	m := Model(cat(node), Options{})
	typ := m.Lookup("Node")
	if typ.Base != "" {
		t.Errorf("Base = %q, want none", typ.Base)
	}
	if len(typ.Fields) != 1 || typ.Fields[0].TypeName != "Node" {
		t.Errorf("fields = %+v, want a nested Node", typ.Fields)
	}
}
