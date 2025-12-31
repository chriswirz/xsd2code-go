// Package pgintro reads a live PostgreSQL database and describes it as the
// same model an XML schema produces, so that a database can be turned into an
// XSD or straight into data classes.
//
// The mapping is the reverse of the one internal/ir applies when it generates
// DDL, and a schema taken through generate and then back through here comes
// out recognisably the same: a foreign key is nested content, a link table is
// repeated content, an array column is a repeated value, and a CHECK
// constraint over a text column is an enumeration.
package pgintro

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Options controls introspection.
type Options struct {
	// DSN is the connection string: a URL, or libpq keyword/value pairs.
	DSN string
	// Schema is the Postgres schema to read. Empty means "public".
	Schema string
	// Views includes views and materialized views alongside ordinary tables.
	Views bool
	// Keys keeps the surrogate primary keys as content. They are dropped by
	// default: a generated identity column is a fact about the database, not
	// about the documents it holds.
	Keys bool
	// TargetNamespace is the namespace of the resulting model.
	TargetNamespace string
}

// catalog is the raw shape of the database, before it is interpreted.
type catalog struct {
	tables []*table
	byName map[string]*table
	// enums maps a Postgres enum type name to its labels, in sort order.
	enums map[string][]string
}

type table struct {
	name string
	doc  string
	// kind is the pg_class relkind: r and p are tables, v and m are views.
	kind string

	columns []*column
	// pk lists the primary key columns, in index order.
	pk []string
	// foreign keys declared on this table, single-column only.
	fkeys []*fkey
	// checks maps a column name to the values a CHECK constraint pins it to.
	checks map[string][]string
}

type column struct {
	name string
	// sqlType is the fully rendered type, "character varying(20)" and all.
	sqlType string
	// baseType is the type with any array brackets and length removed, which
	// is what the type mapping keys on.
	baseType string
	// pgType is the internal type name from pg_type, which is how a column
	// bound to an enum type is recognized. An array's is the element type with
	// a leading underscore, as Postgres names it.
	pgType  string
	array   bool
	notNull bool
	// generated is set for an identity column or one defaulting to nextval:
	// the marks of a surrogate key.
	generated bool
	def       string
	doc       string
}

type fkey struct {
	column   string
	refTable string
	refCol   string
}

// isTable reports whether the relation holds rows of its own, which decides
// whether keys and constraints are worth looking for.
func (t *table) isTable() bool { return t.kind == "r" || t.kind == "p" }

// column finds a column by name.
func (t *table) column(name string) *column {
	for _, c := range t.columns {
		if c.name == name {
			return c
		}
	}
	return nil
}

// fkeyFor returns the foreign key declared on a column, or nil.
func (t *table) fkeyFor(name string) *fkey {
	for _, f := range t.fkeys {
		if f.column == name {
			return f
		}
	}
	return nil
}

// read pulls the whole catalog in a handful of queries. One query per kind of
// object, rather than one per table, keeps introspection to a constant number
// of round trips however large the database is.
func read(ctx context.Context, conn *pgx.Conn, opts Options) (*catalog, error) {
	kinds := []string{"r", "p"}
	if opts.Views {
		kinds = append(kinds, "v", "m")
	}
	schema := opts.Schema
	if schema == "" {
		schema = "public"
	}

	c := &catalog{byName: map[string]*table{}, enums: map[string][]string{}}

	rows, err := conn.Query(ctx, qTables, schema, kinds)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	for rows.Next() {
		t := &table{checks: map[string][]string{}}
		var doc *string
		if err := rows.Scan(&t.name, &t.kind, &doc); err != nil {
			return nil, err
		}
		if doc != nil {
			t.doc = *doc
		}
		c.tables = append(c.tables, t)
		c.byName[t.name] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(c.tables) == 0 {
		return nil, fmt.Errorf("schema %q contains no tables", schema)
	}

	rows, err = conn.Query(ctx, qColumns, schema, kinds)
	if err != nil {
		return nil, fmt.Errorf("listing columns: %w", err)
	}
	for rows.Next() {
		var tableName, typeName, identity string
		col := &column{}
		var doc, def *string
		if err := rows.Scan(&tableName, &col.name, &col.sqlType, &typeName,
			&col.notNull, &identity, &def, &doc); err != nil {
			return nil, err
		}
		t := c.byName[tableName]
		if t == nil {
			continue
		}
		if doc != nil {
			col.doc = *doc
		}
		if def != nil {
			col.def = *def
		}
		col.array = strings.HasSuffix(col.sqlType, "[]")
		col.baseType = baseTypeName(col.sqlType)
		col.pgType = strings.TrimPrefix(typeName, "_")
		// An identity column, or one defaulting to a sequence, is a surrogate
		// key wherever it also happens to be the primary key.
		col.generated = identity != "" || strings.Contains(col.def, "nextval(")
		t.columns = append(t.columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = conn.Query(ctx, qPrimaryKeys, schema)
	if err != nil {
		return nil, fmt.Errorf("listing primary keys: %w", err)
	}
	for rows.Next() {
		var tableName, colName string
		if err := rows.Scan(&tableName, &colName); err != nil {
			return nil, err
		}
		if t := c.byName[tableName]; t != nil {
			t.pk = append(t.pk, colName)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = conn.Query(ctx, qForeignKeys, schema)
	if err != nil {
		return nil, fmt.Errorf("listing foreign keys: %w", err)
	}
	for rows.Next() {
		var tableName string
		f := &fkey{}
		if err := rows.Scan(&tableName, &f.column, &f.refTable, &f.refCol); err != nil {
			return nil, err
		}
		if t := c.byName[tableName]; t != nil {
			t.fkeys = append(t.fkeys, f)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = conn.Query(ctx, qChecks, schema)
	if err != nil {
		return nil, fmt.Errorf("listing check constraints: %w", err)
	}
	for rows.Next() {
		var tableName, def string
		if err := rows.Scan(&tableName, &def); err != nil {
			return nil, err
		}
		t := c.byName[tableName]
		if t == nil {
			continue
		}
		if col, values := parseCheckEnum(def); col != "" {
			t.checks[col] = values
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = conn.Query(ctx, qEnums, schema)
	if err != nil {
		return nil, fmt.Errorf("listing enum types: %w", err)
	}
	for rows.Next() {
		var typeName, label string
		if err := rows.Scan(&typeName, &label); err != nil {
			return nil, err
		}
		c.enums[typeName] = append(c.enums[typeName], label)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

// The catalog queries. pg_catalog rather than information_schema throughout:
// it exposes the identity, the comments and the constraint definitions that
// the standard views leave out, and it is what psql itself reads.
const (
	qTables = `
SELECT c.relname, c.relkind::text, obj_description(c.oid, 'pg_class')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = ANY($2::text[]::"char"[])
ORDER BY c.relname`

	qColumns = `
SELECT c.relname,
       a.attname,
       format_type(a.atttypid, a.atttypmod),
       t.typname,
       a.attnotnull,
       a.attidentity::text,
       pg_get_expr(d.adbin, d.adrelid),
       col_description(c.oid, a.attnum)
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
WHERE n.nspname = $1
  AND c.relkind = ANY($2::text[]::"char"[])
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`

	qPrimaryKeys = `
SELECT c.relname, a.attname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
WHERE i.indisprimary AND n.nspname = $1
ORDER BY c.relname, a.attnum`

	// Single-column foreign keys only. A composite key has no sensible reading
	// as one nested element, and the columns stay ordinary values.
	qForeignKeys = `
SELECT c.relname, a.attname, cf.relname, af.attname
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class cf ON cf.oid = con.confrelid
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = con.conkey[1]
JOIN pg_attribute af ON af.attrelid = con.confrelid AND af.attnum = con.confkey[1]
WHERE con.contype = 'f' AND n.nspname = $1 AND array_length(con.conkey, 1) = 1
ORDER BY c.relname, a.attname`

	qChecks = `
SELECT c.relname, pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE con.contype = 'c' AND n.nspname = $1
ORDER BY c.relname, con.conname`

	qEnums = `
SELECT t.typname, e.enumlabel
FROM pg_type t
JOIN pg_enum e ON e.enumtypid = t.oid
JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = $1
ORDER BY t.typname, e.enumsortorder`
)

// checkEnumPattern matches the shape Postgres normalizes an IN list to. A
// constraint written CHECK (status IN ('a','b')) is stored, and read back, as
// CHECK ((status = ANY (ARRAY['a'::text, 'b'::text]))) -- and on a varchar
// column as CHECK (((status)::text = ANY ((ARRAY['a'::character varying, ...
// ])::text[]))), so the column may arrive parenthesized and cast.
var checkEnumPattern = regexp.MustCompile(
	`\(?"?([A-Za-z_][A-Za-z0-9_$]*)"?\)?(?:::[A-Za-z0-9_ ."]+)?\s*=\s*ANY\s*\(\(?\s*ARRAY\[(.*?)\]`)

// checkEnumValue matches one quoted, possibly cast, member of that array.
var checkEnumValue = regexp.MustCompile(`'((?:[^']|'')*)'(?:::[A-Za-z0-9_ ."]+)?`)

// parseCheckEnum recovers the column and the values of a CHECK constraint that
// pins a column to a fixed set. Anything else -- a range check, a length
// check, an expression over several columns -- returns an empty column name
// and is left alone, because only a closed set of values is an enumeration.
func parseCheckEnum(def string) (string, []string) {
	m := checkEnumPattern.FindStringSubmatch(def)
	if m == nil {
		return "", nil
	}
	var values []string
	for _, v := range checkEnumValue.FindAllStringSubmatch(m[2], -1) {
		values = append(values, strings.ReplaceAll(v[1], "''", "'"))
	}
	if len(values) == 0 {
		return "", nil
	}
	return m[1], values
}

// baseTypeName strips the array brackets and any length or precision from a
// rendered type, leaving the name the mapping table is keyed on.
func baseTypeName(sqlType string) string {
	t := strings.TrimSuffix(strings.TrimSpace(sqlType), "[]")
	if i := strings.Index(t, "("); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	// format_type qualifies a type that is not in the search path.
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	return strings.Trim(t, `"`)
}
