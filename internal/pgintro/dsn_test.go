package pgintro

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestNormalizeConnects is the test that matters: every accepted form has to
// come out as a connection to the same place. Checking the normalized string
// alone would only prove that the translation is stable, not that it is right,
// so each one is handed to pgx and the connection it produces is inspected.
func TestNormalizeConnects(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
	}{
		{"uri", "postgres://alice:secret@db.example.com:6543/orders"},
		{"uri, postgresql scheme", "postgresql://alice:secret@db.example.com:6543/orders"},
		{"uri, mixed case scheme", "POSTGRESQL://alice:secret@db.example.com:6543/orders"},
		{"jdbc url", "jdbc:postgresql://db.example.com:6543/orders?user=alice&password=secret"},
		{"libpq pairs", "host=db.example.com port=6543 dbname=orders user=alice password=secret"},
		{"ado.net", "Host=db.example.com;Port=6543;Database=orders;Username=alice;Password=secret"},
		{"ado.net, spelled as .NET documents it", "Server=db.example.com;Port=6543;Database=orders;User ID=alice;Password=secret"},
		{"ado.net, trailing semicolon and spaces", " Host = db.example.com ; Port = 6543 ; Database = orders ; Username = alice ; Password = secret ; "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dsn, err := Normalize(tt.in)
			if err != nil {
				t.Fatalf("Normalize(%q): %v", tt.in, err)
			}
			cfg, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatalf("pgx rejected %q, normalized from %q: %v", dsn, tt.in, err)
			}
			if cfg.Host != "db.example.com" {
				t.Errorf("host = %q, from %q", cfg.Host, dsn)
			}
			if cfg.Port != 6543 {
				t.Errorf("port = %d, from %q", cfg.Port, dsn)
			}
			if cfg.Database != "orders" {
				t.Errorf("dbname = %q, from %q", cfg.Database, dsn)
			}
			if cfg.User != "alice" {
				t.Errorf("user = %q, from %q", cfg.User, dsn)
			}
			if cfg.Password != "secret" {
				t.Errorf("password = %q, from %q", cfg.Password, dsn)
			}
		})
	}
}

// A URI is the form pgx already reads, so it must come back untouched: a
// rewritten URI is a chance to lose a query parameter nobody tested.
func TestNormalizeLeavesAURIAlone(t *testing.T) {
	const uri = "postgres://alice@db.example.com/orders?sslmode=verify-full&application_name=xsd2code"
	got, err := Normalize(uri)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got != uri {
		t.Errorf("Normalize rewrote the URI:\n got %q\nwant %q", got, uri)
	}
}

func TestNormalizeADODetails(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{
			// The pool settings configure the .NET driver and mean nothing to
			// libpq, so they are dropped rather than reported.
			name: "client-only settings are dropped",
			in:   "Host=localhost;Database=orders;Pooling=true;Maximum Pool Size=20;Command Timeout=30",
			want: "host=localhost dbname=orders",
		},
		{
			// .NET runs the two verifying modes together; libpq hyphenates them.
			name: "ssl mode is respelled",
			in:   "Host=localhost;Database=orders;SSL Mode=VerifyFull",
			want: "host=localhost dbname=orders sslmode=verify-full",
		},
		{
			name: "a password may hold a semicolon when it is quoted",
			in:   "Host=localhost;Database=orders;Password='a;b'",
			want: "host=localhost dbname=orders password=a;b",
		},
		{
			name: "a value with a space is quoted for libpq",
			in:   "Host=localhost;Database=orders;Application Name=xsd2code go",
			want: "host=localhost dbname=orders application_name='xsd2code go'",
		},
		{
			// An empty setting says nothing, and passing it on would override a
			// value the environment supplies.
			name: "empty values are left out",
			in:   "Host=localhost;Database=orders;Password=",
			want: "host=localhost dbname=orders",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.in)
			if err != nil {
				t.Fatalf("Normalize(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Normalize(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
			if _, err := pgx.ParseConfig(got); err != nil {
				t.Errorf("pgx rejected %q: %v", got, err)
			}
		})
	}
}

// The whole reason the normalizing exists: pgx accepts these and connects
// somewhere other than where they say, so they have to be refused here.
func TestNormalizeRejectsWhatPgxWouldMisread(t *testing.T) {
	for _, tt := range []struct {
		name    string
		in      string
		mention string
	}{
		{
			// pgx reads this as one keyword, host, whose value is the whole
			// rest of the string: no database, no user, no password.
			name:    "ado.net keywords without semicolons",
			in:      "Host=localhost Database=orders Username=alice",
			mention: "dbname",
		},
		{
			name:    "a keyword that does not exist",
			in:      "host=localhost dbnmae=orders",
			mention: "dbnmae",
		},
		{
			name:    "nothing",
			in:      "   ",
			mention: "no connection string",
		},
		{
			name:    "a bare database name",
			in:      "orders",
			mention: "postgres://",
		},
		{
			name:    "a jdbc url for another database",
			in:      "jdbc:mysql://localhost/orders",
			mention: "JDBC PostgreSQL URL",
		},
		{
			name:    "an ado.net setting with no equals sign",
			in:      "Host=localhost;Database",
			mention: "Key=Value",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.in)
			if err == nil {
				t.Fatalf("Normalize(%q) = %q, want an error", tt.in, got)
			}
			if !strings.Contains(err.Error(), tt.mention) {
				t.Errorf("the error for %q should mention %q, got: %v", tt.in, tt.mention, err)
			}
		})
	}
}

// TestSSLModeSurvivesEveryForm pins what the README promises: an SSL mode
// reaches the driver whichever way the connection is written, and the .NET
// spelling of a mode is respelled rather than passed on as a mode PostgreSQL
// does not have.
func TestSSLModeSurvivesEveryForm(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"postgres://user:pass@host:5432/db?sslmode=require", "require"},
		{"postgres://user:pass@host:5432/db?sslmode=verify-full", "verify-full"},
		{"host=host port=5432 dbname=db user=user sslmode=require", "require"},
		{"Host=host;Port=5432;Database=db;Username=user;SSL Mode=Require", "require"},
		{"Host=host;Port=5432;Database=db;Username=user;SslMode=VerifyCA", "verify-ca"},
		{"Host=host;Port=5432;Database=db;Username=user;ssl mode=VerifyFull", "verify-full"},
		{"Host=host;Port=5432;Database=db;Username=user;SSL Mode=Disable", "disable"},
	} {
		got, err := Normalize(tt.in)
		if err != nil {
			t.Errorf("Normalize(%q): %v", tt.in, err)
			continue
		}
		if !strings.Contains(got, "sslmode="+tt.want) {
			t.Errorf("Normalize(%q) = %q, want it to carry sslmode=%s", tt.in, got, tt.want)
		}
		// A mode is only worth carrying if the driver then acts on it, and
		// disable is the one that is visible in the parsed configuration.
		cfg, err := pgx.ParseConfig(got)
		if err != nil {
			t.Errorf("pgx rejected %q: %v", got, err)
			continue
		}
		if tt.want == "disable" && cfg.TLSConfig != nil {
			t.Errorf("Normalize(%q) still asked for TLS", tt.in)
		}
		if tt.want != "disable" && cfg.TLSConfig == nil {
			t.Errorf("Normalize(%q) did not ask for TLS", tt.in)
		}
	}
}

// The mapping is only useful if the keyword it produces is one libpq has; a
// typo here would be invisible until someone used that setting.
func TestADOKeywordsMapOntoRealKeywords(t *testing.T) {
	for ado, keyword := range adoKeywords {
		if !libpqKeywords[keyword] {
			t.Errorf("%q maps to %q, which is not a libpq keyword", ado, keyword)
		}
	}
}
