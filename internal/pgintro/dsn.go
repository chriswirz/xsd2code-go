package pgintro

import (
	"fmt"
	"sort"
	"strings"
)

// A connection can be written several ways, and the one a caller has to hand
// depends on where they copied it from: a URI out of a container's environment,
// libpq keyword/value pairs out of a shell script, or the semicolon-separated
// form out of an appsettings.json. This file turns all of them into the one
// form pgx parses.
//
// Normalizing is not merely a convenience. pgx accepts a keyword/value string
// and silently ignores every keyword it does not recognize, so the ADO.NET
// string that the generated Entity Framework code connects with --
//
//	Host=localhost;Database=orders;Username=u;Password=p
//
// -- parses without complaint and yields a connection to no named database, as
// the operating system's user, with no password. A tool whose main output is
// C# is going to be handed that string, and connecting to the wrong place is a
// worse outcome than refusing to connect at all.

// Normalize turns a connection string into the form pgx understands: a URI is
// returned unchanged, ADO.NET keyword/value pairs are translated, and anything
// that would parse into a connection the caller did not ask for is rejected
// with a message saying what to write instead.
func Normalize(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("no connection string given")
	}

	// A JDBC URL is a Postgres URI behind a prefix naming the driver, and it is
	// what a reader of a Java configuration has to hand.
	if rest, ok := cutPrefixFold(s, "jdbc:"); ok {
		s = strings.TrimSpace(rest)
		if !isURI(s) {
			return "", fmt.Errorf("%q is not a JDBC PostgreSQL URL; it should read jdbc:postgresql://host/database", s)
		}
	}

	if rest, scheme, ok := cutURIScheme(s); ok {
		// pgx matches the scheme case-sensitively even though a URI scheme is
		// defined not to be, so PostgreSQL:// is lowercased rather than passed
		// on to be rejected.
		return scheme + rest, nil
	}

	// Semicolons separate settings in the ADO.NET form and appear nowhere in
	// the libpq one, where a bare semicolon is part of a value.
	if strings.Contains(s, ";") {
		return fromADO(s)
	}

	if strings.Contains(s, "=") {
		if err := checkKeywords(s); err != nil {
			return "", err
		}
		return s, nil
	}

	return "", fmt.Errorf("%q is not a connection string; write a URI (postgres://user@host/%s), "+
		"libpq pairs (host=... dbname=%s), or an ADO.NET string (Host=...;Database=%s)", s, s, s, s)
}

// isURI reports whether the string opens with a scheme pgx reads as a URI.
func isURI(s string) bool {
	_, _, ok := cutURIScheme(s)
	return ok
}

// cutURIScheme splits a connection URI into its scheme, in the lower case pgx
// insists on, and the rest of the string.
func cutURIScheme(s string) (rest, scheme string, ok bool) {
	for _, candidate := range []string{"postgres://", "postgresql://"} {
		if rest, ok := cutPrefixFold(s, candidate); ok {
			return rest, candidate, true
		}
	}
	return "", "", false
}

// cutPrefixFold is strings.CutPrefix, ignoring case: a scheme is
// case-insensitive, and PostgreSQL:// is a thing people write.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// fromADO translates the semicolon-separated form that .NET uses -- and that
// the generated Entity Framework code connects with -- into libpq pairs.
func fromADO(s string) (string, error) {
	var out []string
	for _, setting := range splitADO(s) {
		key, value, ok := strings.Cut(setting, "=")
		if !ok {
			return "", fmt.Errorf("%q is not a setting: an ADO.NET connection string is Key=Value, separated by semicolons", setting)
		}
		// "User ID" and "user id" are the same setting, and so are "SSL Mode"
		// and "sslmode": .NET compares these without case or spaces.
		name := strings.ToLower(strings.Join(strings.Fields(key), " "))
		value = unquoteADO(strings.TrimSpace(value))

		if adoClientOnly[name] {
			// A pool size or a command timeout describes the .NET client, not
			// the connection, so there is nothing to carry across and nothing
			// lost by leaving it behind.
			continue
		}
		keyword, ok := adoKeywords[name]
		if !ok {
			return "", fmt.Errorf("%q is not a connection setting this understands; "+
				"the ones it maps are %s", key, strings.Join(sortedKeys(adoKeywords), ", "))
		}
		if value == "" {
			continue
		}
		if respell, ok := adoValues[name]; ok {
			// A few settings are spelled differently on each side in their
			// values as well as their names.
			value = respell(value)
		}
		out = append(out, keyword+"="+quoteKeywordValue(value))
	}
	if len(out) == 0 {
		return "", fmt.Errorf("the connection string names no connection settings")
	}
	return strings.Join(out, " "), nil
}

// splitADO breaks the string on the semicolons that separate settings, leaving
// alone any inside a quoted value: a password is entitled to contain one.
func splitADO(s string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
	)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == ';':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())

	var kept []string
	for _, setting := range out {
		// A trailing semicolon is idiomatic in the ADO.NET form, so an empty
		// setting is not an error.
		if strings.TrimSpace(setting) != "" {
			kept = append(kept, strings.TrimSpace(setting))
		}
	}
	return kept
}

// unquoteADO removes the quotes .NET puts around a value that contains a
// semicolon or an equals sign.
func unquoteADO(v string) string {
	if len(v) >= 2 {
		if (v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// quoteKeywordValue wraps a value in the single quotes libpq expects around one
// that is empty or holds a space, escaping what it must.
func quoteKeywordValue(v string) string {
	if v != "" && !strings.ContainsAny(v, " '\\") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(v) + "'"
}

// adoValues holds the settings whose value has to be translated too, keyed by
// the setting name in the form fromADO presents it: lowercased, single-spaced.
// It is the place to add a setting whose two sides do not agree on how a value
// is written; a setting that only needs renaming belongs in adoKeywords alone.
var adoValues = map[string]func(string) string{
	"ssl mode": adoSSLMode,
	"sslmode":  adoSSLMode,
}

// adoSSLMode renders .NET's spelling of an SSL mode as libpq's. The names agree
// apart from case and the two verifying modes, which .NET runs together.
func adoSSLMode(v string) string {
	switch strings.ToLower(strings.Join(strings.Fields(v), "")) {
	case "verifyca":
		return "verify-ca"
	case "verifyfull":
		return "verify-full"
	}
	return strings.ToLower(v)
}

// checkKeywords rejects a keyword/value string that names a setting libpq does
// not have. pgx ignores an unknown keyword rather than reporting it, so
// "Database=orders" -- the .NET spelling of dbname -- otherwise connects
// somewhere the caller never named.
func checkKeywords(s string) error {
	var unknown []string
	for _, pair := range splitKeywordValue(s) {
		key, _, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("%q is not a keyword/value pair", pair)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || libpqKeywords[key] {
			continue
		}
		if keyword, ok := adoKeywords[key]; ok {
			return fmt.Errorf("%q is not a libpq keyword; write %s=... instead, "+
				"or give the whole string in the ADO.NET form with semicolons between the settings", key, keyword)
		}
		unknown = append(unknown, key)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("no PostgreSQL connection keyword is called %s; "+
			"a keyword it does not recognize is one it silently ignores, so it is rejected here instead",
			strings.Join(quoteAll(unknown), " or "))
	}
	return nil
}

// splitKeywordValue breaks a libpq string into its pairs, on the whitespace
// that separates them but not on whitespace inside a quoted value.
func splitKeywordValue(s string) []string {
	var (
		out     []string
		cur     strings.Builder
		quoted  bool
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			cur.WriteRune(r)
			escaped = true
		case r == '\'':
			quoted = !quoted
			cur.WriteRune(r)
		case !quoted && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func quoteAll(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf("%q", n))
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// adoKeywords maps the .NET spelling of a connection setting to libpq's. The
// keys are lowercased and single-spaced, which is how fromADO presents them.
var adoKeywords = map[string]string{
	"host":                      "host",
	"server":                    "host",
	"port":                      "port",
	"database":                  "dbname",
	"initial catalog":           "dbname",
	"username":                  "user",
	"user":                      "user",
	"user id":                   "user",
	"userid":                    "user",
	"password":                  "password",
	"pwd":                       "password",
	"passfile":                  "passfile",
	"application name":          "application_name",
	"timeout":                   "connect_timeout",
	"connection timeout":        "connect_timeout",
	"client encoding":           "client_encoding",
	"encoding":                  "client_encoding",
	"options":                   "options",
	"ssl mode":                  "sslmode",
	"sslmode":                   "sslmode",
	"ssl certificate":           "sslcert",
	"client certificate":        "sslcert",
	"ssl key":                   "sslkey",
	"client certificate key":    "sslkey",
	"ssl password":              "sslpassword",
	"root certificate":          "sslrootcert",
	"target session attributes": "target_session_attrs",
	"service":                   "service",
}

// adoClientOnly are the settings that configure the .NET driver rather than the
// connection: dropping them loses nothing, so they are dropped quietly where an
// unrecognized setting is reported.
var adoClientOnly = map[string]bool{
	"pooling":                     true,
	"minimum pool size":           true,
	"maximum pool size":           true,
	"connection idle lifetime":    true,
	"connection lifetime":         true,
	"connection pruning interval": true,
	"command timeout":             true,
	"internal command timeout":    true,
	"cancellation timeout":        true,
	"max auto prepare":            true,
	"auto prepare min usages":     true,
	"no reset on close":           true,
	"enlist":                      true,
	"persist security info":       true,
	"include error detail":        true,
	"log parameters":              true,
	"multiplexing":                true,
	"write buffer size":           true,
	"read buffer size":            true,
	"socket receive buffer size":  true,
	"socket send buffer size":     true,
	"tcp keepalive":               true,
	"tcp keepalive time":          true,
	"tcp keepalive interval":      true,
	"keepalive":                   true,
}

// libpqKeywords is the set of connection keywords PostgreSQL defines. It is
// spelled out because there is no way to ask the driver: pgx keeps its own
// list private and treats anything outside it as absent rather than as wrong.
var libpqKeywords = map[string]bool{
	"application_name":          true,
	"channel_binding":           true,
	"client_encoding":           true,
	"connect_timeout":           true,
	"dbname":                    true,
	"fallback_application_name": true,
	"gssdelegation":             true,
	"gssencmode":                true,
	"gsslib":                    true,
	"host":                      true,
	"hostaddr":                  true,
	"keepalives":                true,
	"keepalives_count":          true,
	"keepalives_idle":           true,
	"keepalives_interval":       true,
	"krbsrvname":                true,
	"load_balance_hosts":        true,
	"options":                   true,
	"passfile":                  true,
	"password":                  true,
	"port":                      true,
	"replication":               true,
	"require_auth":              true,
	"requirepeer":               true,
	"service":                   true,
	"ssl_max_protocol_version":  true,
	"ssl_min_protocol_version":  true,
	"sslcert":                   true,
	"sslcertmode":               true,
	"sslcompression":            true,
	"sslcrl":                    true,
	"sslcrldir":                 true,
	"sslkey":                    true,
	"sslmode":                   true,
	"sslnegotiation":            true,
	"sslpassword":               true,
	"sslrootcert":               true,
	"sslsni":                    true,
	"target_session_attrs":      true,
	"tcp_user_timeout":          true,
	"user":                      true,
}
