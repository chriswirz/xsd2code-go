package pgintro

import (
	"context"
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/ir"
	"github.com/jackc/pgx/v5"
)

// Introspect connects to a database and describes it as a model.
func Introspect(ctx context.Context, opts Options) (*ir.Model, error) {
	// Every way of writing a connection is turned into the one pgx reads
	// first, and a string that would connect somewhere other than where it
	// says is rejected here rather than at the far end.
	dsn, err := Normalize(opts.DSN)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	defer conn.Close(context.Background())

	cat, err := read(ctx, conn, opts)
	if err != nil {
		return nil, err
	}
	return Model(cat, opts), nil
}

// Model interprets a catalog. It is separated from the reading so the mapping
// can be tested without a server.
func Model(cat *catalog, opts Options) *ir.Model {
	m := ir.NewModel(opts.TargetNamespace)
	b := &builder{cat: cat, opts: opts, model: m, types: map[string]*ir.Type{}}

	b.findLinkTables()
	b.declareTypes()
	b.fillTypes()
	b.declareRoots()
	return m
}

// builder holds the state of one interpretation.
type builder struct {
	cat   *catalog
	opts  Options
	model *ir.Model

	// types maps a table name to the type that stores it.
	types map[string]*ir.Type
	// links maps a link table's name to what it joins.
	links map[string]*link
	// enums maps a Postgres enum type name to the generated enum.
	enums map[string]*ir.Type
	// referenced records the tables some other table points at, which is how
	// the document roots are found.
	referenced map[string]bool
}

// link is a table whose only purpose is to join two others.
type link struct {
	table  *table
	parent *fkey
	child  *fkey
}

// findLinkTables identifies the tables that exist only to join two others, so
// they become repeated content on the parent rather than a type of their own.
// This is the exact inverse of the link tables the DDL generator emits, and it
// also recognizes the hand-written ones, which look the same.
func (b *builder) findLinkTables() {
	b.links = map[string]*link{}
	b.referenced = map[string]bool{}
	for _, t := range b.cat.tables {
		for _, f := range t.fkeys {
			b.referenced[f.refTable] = true
		}
	}
	for _, t := range b.cat.tables {
		if !t.isTable() || len(t.fkeys) != 2 {
			continue
		}
		// Every column has to be one of the two keys or the ordinal that keeps
		// document order. Anything else is an association carrying data of its
		// own, which deserves to stay a type.
		payload := false
		for _, c := range t.columns {
			if t.fkeyFor(c.name) != nil || c.name == "ordinal" {
				continue
			}
			payload = true
		}
		if payload || b.referenced[t.name] {
			continue
		}
		parent, child := b.linkSides(t)
		b.links[t.name] = &link{table: t, parent: parent, child: child}
	}
}

// linkSides decides which of a link table's two foreign keys points at the
// parent -- the row that owns the association -- and which at the child.
//
// The primary key settles it whenever it can: the generated tables are keyed on
// (parent, ordinal), so the parent column is in the key and the child is not.
// That is a structural fact, unlike the table's name, which is ambiguous as
// soon as one table's name is a prefix of another's: items_item_link begins
// with both "items_" and "items_item_".
func (b *builder) linkSides(t *table) (parent, child *fkey) {
	parent, child = t.fkeys[0], t.fkeys[1]
	inKey := func(f *fkey) bool {
		for _, k := range t.pk {
			if k == f.column {
				return true
			}
		}
		return false
	}
	switch {
	case inKey(parent) && !inKey(child):
		return parent, child
	case inKey(child) && !inKey(parent):
		return child, parent
	}
	// A hand-written link table keyed on both columns, or on neither. The
	// longest matching name prefix is the next best evidence; failing that,
	// the order the keys were declared in.
	if prefixLen(t.name, child.refTable) > prefixLen(t.name, parent.refTable) {
		parent, child = child, parent
	}
	return parent, child
}

// prefixLen scores how well a table name is prefixed by a referenced table's
// name, or -1 when it is not.
func prefixLen(name, ref string) int {
	if strings.HasPrefix(name, ref+"_") {
		return len(ref)
	}
	return -1
}

// inheritedFrom reports the table a joined-table subclass extends: one whose
// single-column primary key is also a foreign key onto another table. That is
// the shape the DDL generator emits for an extension, and the shape JPA and EF
// Core both call joined inheritance.
func (b *builder) inheritedFrom(t *table) string {
	if len(t.pk) != 1 {
		return ""
	}
	fk := t.fkeyFor(t.pk[0])
	if fk == nil || fk.refTable == t.name {
		return ""
	}
	// It has to point at the other table's key, not at some other column.
	parent := b.cat.byName[fk.refTable]
	if parent == nil || len(parent.pk) != 1 || parent.pk[0] != fk.refCol {
		return ""
	}
	return fk.refTable
}

// declareTypes creates one type per table, before any of them is filled in, so
// that a reference between two tables can be resolved whichever order they are
// visited in -- and so a cycle terminates.
func (b *builder) declareTypes() {
	b.enums = map[string]*ir.Type{}
	for _, t := range b.cat.tables {
		if _, isLink := b.links[t.name]; isLink {
			continue
		}
		doc := t.doc
		if doc == "" {
			doc = fmt.Sprintf("A row of the %q table.", t.name)
		}
		typ := b.model.AddType(&ir.Type{
			Name:      ir.Pascal(t.name),
			XMLName:   t.name,
			Namespace: b.opts.TargetNamespace,
			Kind:      ir.Class,
			Doc:       doc,
			// The table exists: its name, and the names of its columns, are
			// facts to be carried rather than conventions to be re-derived.
			Table: t.name,
		})
		b.types[t.name] = typ
	}
}

// fillTypes turns each table's columns into fields.
func (b *builder) fillTypes() {
	for _, t := range b.cat.tables {
		typ := b.types[t.name]
		if typ == nil {
			continue
		}
		if base := b.inheritedFrom(t); base != "" {
			if baseType := b.types[base]; baseType != nil {
				typ.Base = baseType.Name
			}
		}
		for _, col := range t.columns {
			f := b.field(t, col)
			if f == nil {
				continue
			}
			typ.Fields = append(typ.Fields, f)
			// A single-column primary key that survived as content is the
			// type's own key. Letting the mapping invent a surrogate beside it
			// would put a second identity on a table that already has one.
			if len(t.pk) == 1 && t.pk[0] == col.name {
				typ.Key = f.Name
			}
		}
		b.addRepeatedContent(t, typ)
	}
}

// field maps one column, or reports nil for a column that is not content.
func (b *builder) field(t *table, col *column) *ir.Field {
	if !b.opts.Keys && b.isSurrogateKey(t, col) {
		// A generated key is an artefact of storage. Keeping it would put a
		// value in the document that the next database to load it would have
		// to ignore.
		return nil
	}
	if len(t.pk) == 1 && t.pk[0] == col.name && b.inheritedFrom(t) != "" {
		// The key of a joined-table subclass is the link to its base, which
		// the type expresses by extending it. As content it would be a nested
		// copy of the very row this one is part of.
		return nil
	}

	f := &ir.Field{
		Name:     ir.Pascal(col.name),
		XMLName:  col.name,
		Column:   col.name,
		Origin:   ir.ElementField,
		Doc:      col.doc,
		Optional: !col.notNull,
	}
	if b.opts.TargetNamespace != "" {
		f.Namespace = b.opts.TargetNamespace
	}
	if d := literalDefault(col.def); d != "" {
		f.Default = d
	}

	// A foreign key is nested content: the row it points at, in place.
	if fk := t.fkeyFor(col.name); fk != nil {
		if target := b.types[fk.refTable]; target != nil {
			f.Name = ir.Pascal(strings.TrimSuffix(col.name, "_id"))
			f.XMLName = strings.TrimSuffix(col.name, "_id")
			f.TypeName = target.Name
			f.Doc = joinDoc(col.doc, fmt.Sprintf("References %s.", fk.refTable))
			return f
		}
	}

	// An array column holds many values of one type.
	if col.array {
		f.Repeated = true
		f.Optional = false
	}

	switch {
	case b.cat.enums[col.pgType] != nil:
		f.TypeName = b.enumType(col.pgType, b.cat.enums[col.pgType], col).Name
	case t.checks[col.name] != nil:
		// A CHECK pinning the column to a fixed set of values is an
		// enumeration written the way a portable schema writes one.
		name := ir.Pascal(t.name) + ir.Pascal(col.name)
		f.TypeName = b.enumType(name, t.checks[col.name], col).Name
	default:
		f.Builtin = builtinFor(col.baseType)
	}
	return f
}

// enumType creates, or reuses, the enum for a set of values.
func (b *builder) enumType(name string, values []string, col *column) *ir.Type {
	if existing, ok := b.enums[name]; ok {
		return existing
	}
	t := &ir.Type{
		Name:        ir.Pascal(name),
		XMLName:     name,
		Namespace:   b.opts.TargetNamespace,
		Kind:        ir.Enum,
		BaseBuiltin: builtinFor(col.baseType),
		Doc:         fmt.Sprintf("The values %s may take.", col.name),
	}
	members := map[string]bool{}
	for _, v := range values {
		member := ir.Pascal(v)
		if member == "" || members[member] {
			member = ir.Pascal(v + fmt.Sprint(len(t.Values)))
		}
		members[member] = true
		t.Values = append(t.Values, ir.EnumValue{Name: member, Value: v})
	}
	b.model.AddType(t)
	b.enums[name] = t
	return t
}

// addRepeatedContent hangs each link table off the parent it belongs to.
func (b *builder) addRepeatedContent(t *table, typ *ir.Type) {
	for _, l := range b.links {
		if l.parent.refTable != t.name {
			continue
		}
		child := b.types[l.child.refTable]
		if child == nil {
			continue
		}
		name := strings.TrimSuffix(l.child.column, "_id")
		typ.Fields = append(typ.Fields, &ir.Field{
			Name:      ir.Pascal(name),
			XMLName:   name,
			Namespace: b.opts.TargetNamespace,
			Origin:    ir.ElementField,
			TypeName:  child.Name,
			Repeated:  true,
			Doc: fmt.Sprintf("Rows of %s joined through %s, in the order the %q column gives.",
				l.child.refTable, l.table.name, "ordinal"),
		})
	}
}

// declareRoots picks the document elements: the tables nothing points at, which
// are the tops of the ownership graph. A schema whose references form a cycle
// has no such table, and then every table is a root -- an arbitrary choice
// would be worse than an inclusive one.
func (b *builder) declareRoots() {
	var roots []*table
	for _, t := range b.cat.tables {
		if b.types[t.name] == nil {
			continue
		}
		if !b.referenced[t.name] {
			roots = append(roots, t)
		}
	}
	if len(roots) == 0 {
		b.model.Warnf("every table is referenced by another, so no table is an obvious document root; all of them are declared as roots")
		for _, t := range b.cat.tables {
			if b.types[t.name] != nil {
				roots = append(roots, t)
			}
		}
	}
	for _, t := range roots {
		typ := b.types[t.name]
		b.model.AddRoot(&ir.Root{
			XMLName:   t.name,
			Namespace: b.opts.TargetNamespace,
			Type:      typ.Name,
			Doc:       typ.Doc,
		})
	}
}

// isSurrogateKey reports whether a column is a generated single-column primary
// key: an identity or a serial, which the DDL generator emits and which no
// document should carry.
func (b *builder) isSurrogateKey(t *table, col *column) bool {
	return len(t.pk) == 1 && t.pk[0] == col.name && col.generated
}

// literalDefault extracts a usable default from a column default expression.
// A function call -- now(), nextval(...), gen_random_uuid() -- describes how
// to make a value, not a value, and has no place in a schema.
func literalDefault(def string) string {
	def = strings.TrimSpace(def)
	if def == "" || strings.Contains(def, "(") {
		return ""
	}
	// Postgres renders a literal default with its cast: 'pending'::text.
	if i := strings.Index(def, "::"); i >= 0 {
		def = def[:i]
	}
	def = strings.TrimSpace(def)
	if len(def) >= 2 && strings.HasPrefix(def, "'") && strings.HasSuffix(def, "'") {
		return strings.ReplaceAll(def[1:len(def)-1], "''", "'")
	}
	switch def {
	case "true", "false", "NULL":
		if def == "NULL" {
			return ""
		}
		return def
	}
	// A bare number is a literal; anything else is an expression.
	for _, r := range def {
		if (r < '0' || r > '9') && r != '.' && r != '-' && r != '+' {
			return ""
		}
	}
	return def
}

func joinDoc(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, " ")
}

// builtins maps Postgres types onto the model's primitives. The mapping is the
// inverse of the one the DDL generator uses, widened to cover the types a
// hand-written database is likely to hold.
var builtins = map[string]ir.Builtin{
	"boolean":                     ir.Bool,
	"bool":                        ir.Bool,
	"smallint":                    ir.Short,
	"int2":                        ir.Short,
	"integer":                     ir.Int,
	"int":                         ir.Int,
	"int4":                        ir.Int,
	"bigint":                      ir.Long,
	"int8":                        ir.Long,
	"smallserial":                 ir.Short,
	"serial":                      ir.Int,
	"bigserial":                   ir.Long,
	"real":                        ir.Float,
	"float4":                      ir.Float,
	"double precision":            ir.Double,
	"float8":                      ir.Double,
	"numeric":                     ir.Decimal,
	"decimal":                     ir.Decimal,
	"money":                       ir.Decimal,
	"timestamp with time zone":    ir.DateTime,
	"timestamptz":                 ir.DateTime,
	"timestamp without time zone": ir.DateTime,
	"timestamp":                   ir.DateTime,
	"date":                        ir.Date,
	"time with time zone":         ir.Time,
	"timetz":                      ir.Time,
	"time without time zone":      ir.Time,
	"time":                        ir.Time,
	"interval":                    ir.Duration,
	"bytea":                       ir.Base64Binary,
	"xml":                         ir.AnyType,
	// A URL is the only thing anyone stores in these, but Postgres does not
	// say so, and xs:anyURI would be a claim the database does not make.
	"text":              ir.String,
	"character varying": ir.String,
	"varchar":           ir.String,
	"character":         ir.String,
	"char":              ir.String,
	"bpchar":            ir.String,
	"name":              ir.String,
	"uuid":              ir.String,
	"json":              ir.String,
	"jsonb":             ir.String,
	"inet":              ir.String,
	"cidr":              ir.String,
	"macaddr":           ir.String,
	"citext":            ir.String,
}

// builtinFor maps a Postgres type, defaulting to a string. A domain, a range,
// a geometry, a user-defined composite: their text form always round-trips,
// and inventing a narrower mapping would lose data.
func builtinFor(pgType string) ir.Builtin {
	if b, ok := builtins[strings.ToLower(pgType)]; ok {
		return b
	}
	return ir.String
}
