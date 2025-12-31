package ir

import (
	"strings"
)

// Relational is the Postgres shape of the model: one table per class, plus the
// link tables that repeated complex content needs. The generators all consume
// this rather than re-deriving a mapping each, so the DDL, the EF Core
// configuration and the JPA annotations cannot disagree about which column a
// field lives in.
type Relational struct {
	Tables []*Table
	// byType finds the table for a model type by its generated name.
	byType map[string]*Table
}

// Table is one generated table.
type Table struct {
	// Name is the snake_case, unquoted table name.
	Name string
	// Type is the model type this table stores.
	Type *Type
	Doc  string

	// Parent is the table of the base class, when the type extends another.
	// Inheritance is mapped joined-table style: the child's primary key is
	// also a foreign key onto the parent's, which is the one mapping both EF
	// Core and JPA express natively and which keeps a base-class query able to
	// see every descendant.
	Parent string

	Columns []*Column
	// Surrogate is set when the primary key was invented here rather than
	// found in the model. A generator emits a key member only for those: a
	// natural key is already one of the type's fields.
	Surrogate bool
	// Links are the association tables for repeated complex fields. They hang
	// off this table but are emitted as tables of their own.
	Links []*LinkTable
}

// Column is one column of a table.
type Column struct {
	Name string
	// Field is the model field this column stores; nil for the synthetic
	// surrogate key.
	Field *Field
	// SQLType is the rendered Postgres type, arrays included.
	SQLType string
	// NotNull is set for required scalar content.
	NotNull bool
	// PrimaryKey marks the surrogate key.
	PrimaryKey bool
	// References names the table a foreign key points at, for a single-valued
	// complex field.
	References string
	// Check is a CHECK constraint body, used to hold an enum column to its
	// declared values without committing the schema to a Postgres enum type,
	// which cannot be altered inside a transaction on older servers and which
	// every ORM maps differently.
	Check string
	Doc   string
}

// LinkTable joins a parent row to the many rows of a repeated complex field.
// Repeated content gets a link table rather than a back-pointer column on the
// child, because the same complex type is routinely reachable from several
// parents and a back-pointer would force one of them to win. The name always
// carries a _link suffix, so it cannot collide with the table of an anonymous
// type that happens to derive the same name.
type LinkTable struct {
	Name string
	// Field is the repeated field the table represents.
	Field *Field
	// Parent and Child are table names.
	Parent string
	Child  string
	// ParentColumn and ChildColumn are the two foreign keys.
	ParentColumn string
	ChildColumn  string
	// ParentType and ChildType are the SQL types of those keys, which follow
	// whatever the two tables use rather than being assumed.
	ParentType string
	ChildType  string
}

// SurrogateKey is the preferred column and member name for a generated primary
// key. XSD models documents, not rows: nothing in a schema is reliably unique,
// so persistence usually needs a key of its own. A model that came from a
// database may already have one, and then its own field wins the name.
const SurrogateKey = "id"

// Key returns the primary key column, which every table has.
func (t *Table) Key() *Column {
	for _, c := range t.Columns {
		if c.PrimaryKey {
			return c
		}
	}
	// Unreachable: a table is never built without a key.
	return &Column{Name: SurrogateKey, SQLType: "bigint", PrimaryKey: true}
}

// Relations builds the relational mapping for a model. tablePrefix is
// prepended to every table name, which is how two schemas can share one
// database without colliding.
func Relations(m *Model, tablePrefix string) *Relational {
	rel := &Relational{byType: map[string]*Table{}}
	names := newUniquer()

	for _, t := range m.Types {
		if t.Kind != Class {
			continue
		}
		name := t.Table
		if name == "" {
			name = Snake(t.Name)
		}
		tbl := &Table{
			Name: names.take(truncate(tablePrefix + name)),
			Type: t,
			Doc:  t.Doc,
		}
		rel.Tables = append(rel.Tables, tbl)
		rel.byType[t.Name] = tbl
	}

	for _, tbl := range rel.Tables {
		t := tbl.Type
		if t.Base != "" {
			if parent, ok := rel.byType[t.Base]; ok {
				tbl.Parent = parent.Name
			}
		}
		// The declared fields claim their column names first. A surrogate key is
		// ours to rename and a real column is not, so if both want "id" it is
		// the invented one that gives way.
		cols := newUniquer()
		for _, f := range t.Fields {
			if f.TypeName != "" {
				if child, ok := rel.byType[f.TypeName]; ok {
					if f.Repeated {
						tbl.Links = append(tbl.Links, &LinkTable{
							Name:         names.take(truncate(tbl.Name + "_" + Snake(f.Name) + "_link")),
							Field:        f,
							Parent:       tbl.Name,
							Child:        child.Name,
							ParentColumn: Snake(tbl.Type.Name) + "_id",
							ChildColumn:  Snake(f.Name) + "_id",
						})
						continue
					}
					fkName := f.Column
					if fkName == "" {
						fkName = Snake(f.Name) + "_id"
					}
					tbl.Columns = append(tbl.Columns, &Column{
						Name:       cols.take(truncate(fkName)),
						Field:      f,
						SQLType:    "bigint",
						References: child.Name,
						NotNull:    !f.Optional,
						Doc:        f.Doc,
					})
					continue
				}
			}
			tbl.Columns = append(tbl.Columns, scalarColumn(m, f, cols))
		}
		rel.addKey(tbl, cols)
	}
	// Foreign key types are settled last: a key column's own type is not known
	// until its table has a key, and a reference may point either way through
	// the list.
	for _, tbl := range rel.Tables {
		if tbl.Parent != "" {
			// A derived table's key is also a foreign key onto its base, so it
			// cannot be a bigint while the base is keyed on something else.
			tbl.Key().SQLType = keyType(rel, tbl.Parent)
		}
		for _, col := range tbl.Columns {
			if col.References == "" {
				continue
			}
			if target := rel.byName(col.References); target != nil {
				col.SQLType = target.Key().SQLType
			}
		}
		for _, link := range tbl.Links {
			link.ParentType = keyType(rel, link.Parent)
			link.ChildType = keyType(rel, link.Child)
		}
	}
	return rel
}

// byName finds a table by its generated table name.
func (r *Relational) byName(name string) *Table {
	for _, t := range r.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// keyType is the SQL type of a table's key, for a column that references it.
func keyType(r *Relational, table string) string {
	if t := r.byName(table); t != nil {
		return t.Key().SQLType
	}
	return "bigint"
}

// addKey gives a table its primary key: the column of the field the model
// nominates, or a surrogate prepended to the front.
func (r *Relational) addKey(tbl *Table, cols *uniquer) {
	if key := tbl.Type.Key; key != "" {
		for _, c := range tbl.Columns {
			if c.Field != nil && c.Field.Name == key {
				c.PrimaryKey = true
				c.NotNull = true
				return
			}
		}
	}
	tbl.Surrogate = true
	// Prepended, because a key belongs at the top of a table definition, and
	// because every generator reads Columns in order.
	tbl.Columns = append([]*Column{{
		Name:       cols.take(SurrogateKey),
		SQLType:    "bigint",
		PrimaryKey: true,
		NotNull:    true,
		Doc:        "Surrogate key. XML documents carry no dependable identity of their own.",
	}}, tbl.Columns...)
}

// scalarColumn maps a primitive, enum or wildcard field to a column.
func scalarColumn(m *Model, f *Field, cols *uniquer) *Column {
	name := f.Column
	if name == "" {
		name = Snake(f.Name)
	}
	col := &Column{
		Name:  cols.take(truncate(name)),
		Field: f,
		Doc:   f.Doc,
	}
	base := "text"
	switch {
	case f.Origin == AnyAttrField:
		// Undeclared attributes are name/value pairs with no schema behind
		// them; jsonb keeps them queryable without inventing columns.
		base = "jsonb"
	case f.Origin == AnyField:
		base = "xml"
	case f.TypeName != "":
		if t := m.Lookup(f.TypeName); t != nil && t.Kind == Enum {
			base = "text"
			col.Check = enumCheck(col.Name, t)
		}
	default:
		base = sqlType(f.Builtin)
	}
	// Both a repeated element and an xs:list hold many values of one primitive;
	// a Postgres array stores them without a table whose only purpose is to
	// keep an ordering.
	if f.Repeated || f.List {
		base += "[]"
		col.Check = "" // a CHECK over an array needs a different form; omit it
	}
	col.SQLType = base
	col.NotNull = !f.Optional && !f.Repeated && f.Origin != AnyField && f.Origin != AnyAttrField
	return col
}

// enumCheck renders the CHECK constraint that pins an enum column to the
// values the schema declared.
func enumCheck(col string, t *Type) string {
	if len(t.Values) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(t.Values))
	for _, v := range t.Values {
		quoted = append(quoted, "'"+strings.ReplaceAll(v.Value, "'", "''")+"'")
	}
	return quote(col) + " IN (" + strings.Join(quoted, ", ") + ")"
}

// sqlType maps a primitive onto Postgres.
func sqlType(b Builtin) string {
	switch b {
	case Bool:
		return "boolean"
	case Byte, Short, UnsignedByte:
		return "smallint"
	case Int, UnsignedShort:
		return "integer"
	case Long, UnsignedInt:
		return "bigint"
	case UnsignedLong:
		return "numeric(20,0)"
	case Float:
		return "real"
	case Double:
		return "double precision"
	case Decimal:
		return "numeric"
	case DateTime:
		return "timestamptz"
	case Date:
		return "date"
	case Time:
		return "time"
	case Duration:
		return "interval"
	case Base64Binary, HexBinary:
		return "bytea"
	case AnyType:
		return "xml"
	}
	return "text"
}

// Table returns the table storing the named model type, or nil.
func (r *Relational) Table(typeName string) *Table {
	return r.byType[typeName]
}

// Snake renders an identifier as lower_snake_case, the spelling Postgres
// treats case-insensitively and therefore the only one that survives an
// unquoted round trip.
func Snake(s string) string {
	words := splitWords(s)
	for i, w := range words {
		words[i] = strings.ToLower(w)
	}
	out := strings.Join(words, "_")
	if out == "" {
		return "value"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "n" + out
	}
	if reservedSQL[out] {
		out += "_"
	}
	return out
}

// truncate holds an identifier inside Postgres's 63-byte limit. A name that is
// silently cut off by the server would break every later reference to it.
func truncate(s string) string {
	const maxIdent = 63
	if len(s) <= maxIdent {
		return s
	}
	return s[:maxIdent]
}

// quote renders an identifier for DDL.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// reservedSQL are the words that cannot be an unquoted identifier. Everything
// is quoted in the emitted DDL anyway, but a suffixed name keeps the generated
// ORM code and hand-written queries readable.
var reservedSQL = map[string]bool{
	"all": true, "analyse": true, "analyze": true, "and": true, "any": true,
	"array": true, "as": true, "asc": true, "authorization": true, "binary": true,
	"both": true, "case": true, "cast": true, "check": true, "collate": true,
	"column": true, "constraint": true, "create": true, "cross": true,
	"current_date": true, "current_time": true, "current_timestamp": true,
	"current_user": true, "default": true, "deferrable": true, "desc": true,
	"distinct": true, "do": true, "else": true, "end": true, "except": true,
	"false": true, "for": true, "foreign": true, "from": true, "grant": true,
	"group": true, "having": true, "in": true, "initially": true, "inner": true,
	"intersect": true, "into": true, "is": true, "join": true, "leading": true,
	"left": true, "like": true, "limit": true, "localtime": true,
	"localtimestamp": true, "natural": true, "not": true, "null": true,
	"offset": true, "on": true, "only": true, "or": true, "order": true,
	"outer": true, "overlaps": true, "placing": true, "primary": true,
	"references": true, "returning": true, "right": true, "select": true,
	"session_user": true, "similar": true, "some": true, "symmetric": true,
	"table": true, "then": true, "to": true, "trailing": true, "true": true,
	"union": true, "unique": true, "user": true, "using": true, "variadic": true,
	"verbose": true, "when": true, "where": true, "window": true, "with": true,
}
