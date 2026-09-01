// Command xsd2code-go turns XML schemas into data classes for C#, Java, Go,
// Kotlin, Swift, TypeScript, JavaScript, Python, Rust and C++, and infers a
// schema from example documents or from a live PostgreSQL database.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chriswirz/xsd2code-go/internal/gen"
	"github.com/chriswirz/xsd2code-go/internal/infer"
	"github.com/chriswirz/xsd2code-go/internal/ir"
	"github.com/chriswirz/xsd2code-go/internal/pgintro"
	"github.com/chriswirz/xsd2code-go/internal/xsd"
	"github.com/chriswirz/xsd2code-go/internal/xsdout"
)

// version is stamped by the release build with -ldflags -X main.version.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "xsd2code-go: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return fmt.Errorf("no command given")
	}
	switch args[0] {
	case "generate", "gen":
		return generate(args[1:])
	case "infer":
		return inferCmd(args[1:])
	case "version", "--version", "-v":
		fmt.Println("xsd2code-go " + version)
		return nil
	case "help", "--help", "-h":
		if len(args) > 1 {
			// "help generate" is the same thing as "generate -h", and someone
			// will type each of them.
			return run([]string{args[1], "-h"})
		}
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `xsd2code-go %s -- XML Schema to code, and back.

USAGE
  xsd2code-go generate [flags] <schema.xsd>...   generate data classes
  xsd2code-go generate [flags] --database <dsn>  generate them from a database
  xsd2code-go infer    [flags] <document.xml>... infer a schema from documents
  xsd2code-go infer    [flags] --database <dsn>  infer a schema from a database
  xsd2code-go version                            print the version
  xsd2code-go help [command]                     print this, or a command's flags

DESCRIPTION
  generate reads XML Schema documents and writes data classes that deserialize
  conforming XML through the target language's standard binding, in one call.
  With --postgres, which is on by default, the same run also emits the DDL to
  store those objects and annotates the classes to match it.

  infer goes the other way. Given example documents it derives a schema that
  describes them; given a database it describes that instead, mapping foreign
  keys to nested content and link tables to repeated content.

  Both commands take --database, so a database can be turned into a schema, or
  straight into classes, without a schema file in between.

LANGUAGES
  %s

EXAMPLES
  # C# classes with Entity Framework mapping and the Postgres DDL
  xsd2code-go generate --lang csharp --namespace Contoso.Orders --out ./gen order.xsd

  # The same, serializing without reflection
  xsd2code-go generate --lang csharp --ixmlserializable --namespace Contoso.Orders order.xsd

  # Go structs, no persistence mapping
  xsd2code-go generate --lang go --package orders --postgres=false --out ./orders order.xsd

  # Derive a schema from a directory of samples, then generate from it
  xsd2code-go infer --out order.xsd samples/
  xsd2code-go generate --lang java --package com.contoso.orders order.xsd

  # Describe a live database as a schema, then as TypeScript
  xsd2code-go infer --database "postgres://user:pw@localhost/orders" --out db.xsd
  xsd2code-go generate --lang typescript --database "postgres://localhost/orders" --out ./src

SEE ALSO
  man xsd2code-go, and https://github.com/chriswirz/xsd2code-go
`, version, strings.Join(gen.Languages(), ", "))
}

// generate implements the generate subcommand.
func generate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	lang := fs.String("lang", "go", "target language: "+strings.Join(gen.Languages(), ", "))
	out := fs.String("out", ".", "directory to write the generated files into")
	pkg := fs.String("package", "", "Go, Java, Kotlin or Rust package, or JS/TS module name")
	namespace := fs.String("namespace", "", "C# or C++ namespace (alias for -package)")
	postgres := fs.Bool("postgres", true, "add Postgres persistence mapping and emit schema.sql")
	prefix := fs.String("table-prefix", "", "prefix for every generated table name")
	ixml := fs.Bool("ixmlserializable", false,
		"C# only: emit classes that implement IXmlSerializable, so the consuming application serializes without reflection")
	stdout := fs.Bool("stdout", false, "write to standard output instead of files")
	db := fs.String("database", "",
		"read the model from this PostgreSQL database instead of a schema file: a URI, libpq pairs, or an ADO.NET string")
	dbSchema := fs.String("db-schema", "public", "the Postgres schema to read, with --database")
	dbViews := fs.Bool("db-views", false, "include views and materialized views, with --database")
	dbKeys := fs.Bool("db-keys", false, "keep generated surrogate keys as content, with --database")
	targetNS := fs.String("target-namespace", "", "XML namespace for a model read with --database")

	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: xsd2code-go generate [flags] <schema.xsd>...
       xsd2code-go generate [flags] --database <dsn>

Reads XML Schema documents -- following xs:include and xs:import from disk --
and writes data classes that deserialize conforming XML through the target
language's standard binding.

xs:include and xs:import are resolved relative to the importing document. An
import whose schema is not present is reported and its types become strings,
rather than failing the run; nothing is fetched over the network.

FLAGS
`)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, `
OUTPUT
  go          models.go, xsdtypes.go              encoding/xml, plus json and db tags
  csharp      Models.cs, <Namespace>DbContext.cs  XmlSerializer, EF Core
              --ixmlserializable makes Models.cs implement IXmlSerializable
              instead: generated readers and writers over XmlReader and
              XmlWriter, no reflection, so the types survive trimming and
              NativeAOT. The classes keep the same shape either way.
  java        one .java per type, in the package  JAXB (jakarta.xml.bind), JPA
  kotlin      Models.kt, in the package           data classes over the JDK's DOM
  swift       Models.swift                        structs over XMLParser
  typescript  models.ts                           readers over the DOM
  javascript  models.js, models.d.ts              the same, typed by declaration
  python      models.py                           dataclasses over ElementTree
  rust        models.rs                           serde with quick-xml
  cpp         models.hpp, models.cpp              pugixml
  --postgres  schema.sql                          the DDL, for every language

  Go, Python, Kotlin and Swift need nothing beyond their standard library.
  The others name their dependencies in a header comment at the top of the
  generated file.

MAPPING
  complexType                a class
  extension                  inheritance where the language has it; Kotlin,
                             Swift and Rust cannot, so the inherited members
                             are written into each derived type
  restriction                a flattened standalone class
  simpleContent              a Value member plus the attributes
  simpleType + enumeration   an enum carrying each XML lexical value
  group, attributeGroup      expanded inline at the point of reference
  choice                     optional members, documented as exclusive
  minOccurs="0"              nullable, so absent and zero stay distinct; on a
                             C# attribute, the XmlSerializer Specified
                             companion, which is what it accepts there
  maxOccurs>1                a collection, never also nullable
  list                       one element or attribute split on whitespace
  any, anyAttribute          raw XML, and name/value pairs

Anything that had to be approximated is printed as a warning.
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var (
		model  *ir.Model
		source string
	)
	if *db != "" {
		if len(fs.Args()) > 0 {
			return fmt.Errorf("--database reads the model from a database; do not also name schema files")
		}
		m, err := introspect(*db, *dbSchema, *targetNS, *dbViews, *dbKeys)
		if err != nil {
			return err
		}
		model, source = m, "the "+*dbSchema+" schema of a PostgreSQL database"
	} else {
		paths, err := expand(fs.Args(), ".xsd")
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			fs.Usage()
			return fmt.Errorf("no schema files given")
		}
		set, err := xsd.Load(paths...)
		if err != nil {
			return err
		}
		model, source = ir.Build(set), strings.Join(baseNames(paths), " ")
	}
	warn(model.Warnings)
	if len(model.Types) == 0 {
		return fmt.Errorf("nothing to generate: the model declares no types")
	}

	name := *pkg
	if name == "" {
		name = *namespace
	}
	files, err := gen.Generate(model, gen.Options{
		Language:    *lang,
		Package:     name,
		Postgres:    *postgres,
		TablePrefix: *prefix,
		Source:      source,

		XmlSerializable: *ixml,
	})
	if err != nil {
		return err
	}

	if *stdout {
		for _, f := range files {
			fmt.Printf("// ==== %s ====\n%s\n", f.Name, f.Content)
		}
		return nil
	}
	return write(*out, files)
}

// inferCmd implements the infer subcommand.
func inferCmd(args []string) error {
	opts := infer.DefaultOptions()
	fs := flag.NewFlagSet("infer", flag.ContinueOnError)
	out := fs.String("out", "", "file to write the schema to (default: standard output)")
	fs.StringVar(&opts.TargetNamespace, "target-namespace", "",
		"target namespace for the schema (default: the namespace of the first document)")
	fs.IntVar(&opts.MaxEnum, "max-enum", opts.MaxEnum,
		"treat a field with at most this many distinct values as an enumeration; 0 disables it")
	fs.IntVar(&opts.MinEnumSamples, "min-enum-samples", opts.MinEnumSamples,
		"how many values must be seen before an enumeration is inferred")
	fs.BoolVar(&opts.Strings, "strings", false,
		"give every value xs:string instead of inferring a datatype")
	db := fs.String("database", "",
		"describe this PostgreSQL database instead of XML documents: a URI, libpq pairs, or an ADO.NET string")
	dbSchema := fs.String("db-schema", "public", "the Postgres schema to read, with --database")
	dbViews := fs.Bool("db-views", false, "include views and materialized views, with --database")
	dbKeys := fs.Bool("db-keys", false, "keep generated surrogate keys as content, with --database")

	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: xsd2code-go infer [flags] <document.xml|directory>...
       xsd2code-go infer [flags] --database <dsn>

Derives an XML Schema. From documents, it describes the samples given; from a
database, it describes the tables.

A directory argument means every .xml file in it. Globs are expanded by the
tool itself, so samples/*.xml works on Windows too.

FLAGS
`)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, `
FROM DOCUMENTS
  Structure is merged by element name, so an element appearing in three places
  produces one type, not three.

  minOccurs is 0 unless the child appeared in every instance of its parent;
  maxOccurs is unbounded if any single parent held more than one. An attribute
  is required only if it was present on every occurrence.

  Order stays an xs:sequence as long as every document's children are a
  subsequence of the order first seen. Documents that genuinely disagree
  produce a repeated xs:choice, which accepts any interleaving.

  Datatypes are inferred subtractively: a type stays a candidate until a value
  rules it out, so one "N/A" in a column of numbers demotes it to a string.
  0 and 1 are integers unless something spelled out true or false.

  Enumerations need repetition, not merely few values: a field whose every
  value is distinct is an identifier, and is left alone.

  The result describes the samples, not the format. Read it before treating it
  as a contract.

FROM A DATABASE
  --database takes a connection in any of the forms one is usually written in:
  a URI (postgres://user:pw@host/db, and postgresql:// or a jdbc: prefix too),
  libpq keyword/value pairs (host=... dbname=...), or the semicolon-separated
  ADO.NET form the generated C# connects with (Host=...;Database=...). The
  last is translated rather than guessed at: a setting that names nothing
  PostgreSQL has is reported, because the driver would otherwise ignore it and
  connect somewhere else.

  PGPASSWORD, PGSERVICE and ~/.pgpass are honoured.

  One complex type per table. A single-column foreign key becomes nested
  content: the row it points at, in place. A table that exists only to join two
  others -- two foreign keys, nothing else but an ordinal -- becomes repeated
  content on the parent. An array column becomes a repeated value. A CHECK
  constraint pinning a column to a fixed set, or a Postgres enum type, becomes
  an enumeration. Column and table comments become xs:documentation.

  Generated surrogate keys are dropped, since they describe the storage rather
  than the document; --db-keys keeps them. Tables that nothing references are
  declared as the document roots.
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var (
		doc      string
		warnings []string
	)
	if *db != "" {
		if len(fs.Args()) > 0 {
			return fmt.Errorf("--database describes a database; do not also name XML files")
		}
		model, err := introspect(*db, *dbSchema, opts.TargetNamespace, *dbViews, *dbKeys)
		if err != nil {
			return err
		}
		doc = xsdout.Write(model, xsdout.Options{
			TargetNamespace: opts.TargetNamespace,
			Header: []string{
				fmt.Sprintf("Generated by xsd2code-go from the %q schema of a PostgreSQL", *dbSchema),
				"database on " + time.Now().Format("2006-01-02") + ".",
				"",
				"It describes the tables as they are now. A foreign key is nested",
				"content, a link table is repeated content, and a CHECK constraint",
				"over a fixed set of values is an enumeration.",
			},
		})
		warnings = model.Warnings
	} else {
		paths, err := expand(fs.Args(), ".xml")
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			fs.Usage()
			return fmt.Errorf("no XML files given")
		}
		schema := infer.New(opts)
		for _, p := range paths {
			if err := schema.AddFile(p); err != nil {
				return err
			}
		}
		doc, warnings = schema.XSD(), schema.Warnings
	}
	warn(warnings)

	if *out == "" {
		fmt.Print(doc)
		return nil
	}
	if dir := filepath.Dir(*out); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "wrote "+*out)
	return nil
}

// introspect reads a model from a database.
func introspect(dsn, schema, namespace string, views, keys bool) (*ir.Model, error) {
	// A connection that is going to fail should fail promptly: a typo in a
	// host name otherwise hangs for the operating system's whole TCP timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return pgintro.Introspect(ctx, pgintro.Options{
		DSN:             dsn,
		Schema:          schema,
		Views:           views,
		Keys:            keys,
		TargetNamespace: namespace,
	})
}

func warn(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
}

// write puts the generated files under dir, creating the directories a
// language's layout requires -- Java's package directories, in particular.
func write(dir string, files []gen.File) error {
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, f.Content, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote "+path)
	}
	return nil
}

// expand resolves the arguments to file paths, expanding any globs itself.
// The Windows shell does not expand them, and a tool that works with
// samples/*.xml on one platform and not the other is a nuisance.
func expand(args []string, dirExt string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	for _, arg := range args {
		matches := []string{arg}
		if strings.ContainsAny(arg, "*?[") {
			var err error
			matches, err = filepath.Glob(arg)
			if err != nil {
				return nil, fmt.Errorf("bad pattern %q: %w", arg, err)
			}
			// Globs are sorted so a run over a directory is reproducible.
			sort.Strings(matches)
			if len(matches) == 0 {
				return nil, fmt.Errorf("no files match %q", arg)
			}
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				add(m)
				continue
			}
			// A directory means "every document in it", which is how a corpus
			// of samples usually arrives.
			entries, err := filepath.Glob(filepath.Join(m, "*"+dirExt))
			if err != nil {
				return nil, err
			}
			sort.Strings(entries)
			if len(entries) == 0 {
				return nil, fmt.Errorf("no %s files in %s", dirExt, m)
			}
			for _, e := range entries {
				add(e)
			}
		}
	}
	return out, nil
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}
