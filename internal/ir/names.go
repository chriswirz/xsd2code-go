package ir

import (
	"strconv"
	"strings"
	"unicode"
)

// Pascal turns an XML name into a PascalCase identifier. XML names routinely
// arrive as "order-id", "ORDER_ID", "orderID" or "x:orderId", and all four
// have to land on the same shape without mangling the acronyms people expect
// to keep, so the split is on separators and case boundaries both.
func Pascal(s string) string {
	words := splitWords(s)
	var b strings.Builder
	for _, w := range words {
		b.WriteString(capitalize(w))
	}
	out := b.String()
	if out == "" {
		return "Item"
	}
	// An identifier may not start with a digit in any of the target languages.
	if r := []rune(out)[0]; unicode.IsDigit(r) {
		out = "N" + out
	}
	return out
}

// Camel is Pascal with a lowercase first word, for Java fields and JSON-ish
// conventions.
func Camel(s string) string {
	p := Pascal(s)
	if p == "" {
		return p
	}
	r := []rune(p)
	// Leading acronyms lowercase whole: "XMLName" -> "xmlName".
	i := 0
	for i < len(r) && unicode.IsUpper(r[i]) {
		i++
	}
	if i > 1 && i < len(r) {
		i-- // keep the last upper as the start of the next word
	}
	for j := 0; j < i; j++ {
		r[j] = unicode.ToLower(r[j])
	}
	return string(r)
}

// ScreamingSnake renders an enum member the way C-family constants are
// conventionally spelled: ORDER_ID.
func ScreamingSnake(s string) string {
	words := splitWords(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w)
	}
	out := strings.Join(words, "_")
	if out == "" {
		return "VALUE"
	}
	if unicode.IsDigit([]rune(out)[0]) {
		out = "N" + out
	}
	return out
}

// splitWords breaks an arbitrary XML name into lowercase-normalized words.
func splitWords(s string) []string {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:] // drop any namespace prefix
	}
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// A lower-to-upper boundary starts a word ("orderId"), and so does
			// an upper followed by a lower when the previous rune was also
			// upper ("XMLName" -> "XML", "Name").
			if i > 0 && unicode.IsUpper(r) {
				prev := runes[i-1]
				next := rune(0)
				if i+1 < len(runes) {
					next = runes[i+1]
				}
				if !unicode.IsUpper(prev) && (unicode.IsLetter(prev) || unicode.IsDigit(prev)) {
					flush()
				} else if unicode.IsUpper(prev) && unicode.IsLower(next) {
					flush()
				}
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	// An acronym inside a mixed-case name is kept as it was written, so
	// "USAddress" stays "USAddress" rather than becoming "UsAddress". A name
	// that is entirely uppercase carries no such signal -- "ORDER_ID" is just
	// a spelling convention -- so it is lowercased and recapitalized.
	shout := isAllUpper(strings.Join(words, ""))
	for i, w := range words {
		switch {
		case shout:
			words[i] = strings.ToLower(w)
		case isAllUpper(w):
			// left as written
		default:
			words[i] = strings.ToLower(w[:1]) + w[1:]
		}
	}
	return words
}

func isAllUpper(s string) bool {
	has := false
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			has = true
		}
	}
	return has
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// uniquer hands out identifiers that are unique within one scope, appending a
// numeric suffix when a name is already taken. Collisions are ordinary here:
// an element and an attribute may share a name on the same type, and two
// anonymous types may derive the same synthesized name.
type uniquer struct {
	seen map[string]bool
}

func newUniquer() *uniquer { return &uniquer{seen: map[string]bool{}} }

func (u *uniquer) take(name string) string {
	if name == "" {
		name = "Item"
	}
	if !u.seen[strings.ToLower(name)] {
		u.seen[strings.ToLower(name)] = true
		return name
	}
	for i := 2; ; i++ {
		cand := name + strconv.Itoa(i)
		if !u.seen[strings.ToLower(cand)] {
			u.seen[strings.ToLower(cand)] = true
			return cand
		}
	}
}

// reserve marks a name as taken without generating one.
func (u *uniquer) reserve(name string) { u.seen[strings.ToLower(name)] = true }
