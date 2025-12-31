package infer

import (
	"sort"
	"strconv"
	"strings"
)

// Values accumulates the sample values of one attribute or text node and
// decides what datatype describes them all.
type Values struct {
	// Count is how many values were seen, empty ones included.
	Count int
	// Empty counts values that were the empty string.
	Empty int

	// candidates holds the datatypes that every value so far has matched.
	// Inference is subtractive: a type stays a candidate until a value rules
	// it out, so a single "N/A" in a column of numbers correctly demotes the
	// whole thing to a string.
	candidates map[string]bool
	// wordBoolean records whether a true/false spelling was ever seen, which
	// is the only evidence that a column of 0s and 1s is really a boolean.
	wordBoolean bool

	// distinct holds the distinct values, up to a cap. Past the cap the map
	// stops growing and overflow is set: keeping every distinct value of a
	// free-text field across a large corpus would cost memory for an answer
	// already known to be "not an enumeration".
	distinct map[string]int
	overflow bool
	// order preserves first-seen order so an inferred enumeration lists its
	// values the way the documents did.
	order []string
}

// distinctCap is the point past which a field is certainly not an enumeration.
const distinctCap = 256

// The datatypes inference will consider, most specific first. The order is the
// answer to "which type wins when a value matches several": a column of 1s and
// 0s is an integer unless something in it spelled out true or false, and a
// column of integers is an integer rather than the decimal it also satisfies.
var candidateOrder = []string{
	"boolean", "long", "decimal", "dateTime", "date", "time", "anyURI", "string",
}

func newValues() *Values {
	c := make(map[string]bool, len(candidateOrder))
	for _, k := range candidateOrder {
		c[k] = true
	}
	return &Values{candidates: c, distinct: map[string]int{}}
}

// observe folds one sample value in.
func (v *Values) observe(s string) {
	v.Count++
	if s == "" {
		v.Empty++
		// An empty value is compatible with a string and nothing else; a
		// required int cannot be blank.
		for k := range v.candidates {
			if k != "string" {
				v.candidates[k] = false
			}
		}
		return
	}
	if !v.overflow {
		if _, ok := v.distinct[s]; !ok {
			if len(v.distinct) >= distinctCap {
				v.overflow = true
			} else {
				v.distinct[s] = 0
				v.order = append(v.order, s)
			}
		}
		v.distinct[s]++
	}
	for _, k := range candidateOrder {
		if v.candidates[k] && !matches(k, s) {
			v.candidates[k] = false
		}
	}
	if s == "true" || s == "false" {
		v.wordBoolean = true
	}
}

// Type returns the XSD datatype local name that fits every observed value.
func (v *Values) Type(plain bool) string {
	if plain || v.Count == 0 {
		return "string"
	}
	for _, k := range candidateOrder {
		if !v.candidates[k] {
			continue
		}
		// 0 and 1 satisfy xs:boolean, but a column of them is an integer far
		// more often than a flag. Only a spelled-out true or false is taken as
		// proof.
		if k == "boolean" && !v.wordBoolean {
			continue
		}
		return k
	}
	return "string"
}

// Enum returns the distinct values when they look like an enumeration, and nil
// otherwise.
func (v *Values) Enum(opts Options) []string {
	if opts.MaxEnum <= 0 || v.overflow || v.Count < opts.MinEnumSamples {
		return nil
	}
	if len(v.distinct) == 0 || len(v.distinct) > opts.MaxEnum {
		return nil
	}
	// A field whose every value is distinct is an identifier, not an
	// enumeration, however few samples there are.
	if len(v.distinct) == v.Count {
		return nil
	}
	// Numbers and dates are ranges that happen to have few samples; only
	// string-valued fields are treated as enumerations.
	if v.Type(opts.Strings) != "string" {
		return nil
	}
	out := append([]string(nil), v.order...)
	sort.Strings(out)
	return out
}

// matches reports whether one value is a valid lexical form of a datatype.
func matches(kind, s string) bool {
	switch kind {
	case "boolean":
		switch s {
		case "true", "false", "0", "1":
			return true
		}
		return false
	case "long":
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil
	case "decimal":
		// Not ParseFloat: it accepts "1e9", "NaN" and "Inf", none of which is a
		// valid xs:decimal.
		body := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
		if body == "" {
			return false
		}
		dot := false
		for _, r := range body {
			switch {
			case r >= '0' && r <= '9':
			case r == '.' && !dot:
				dot = true
			default:
				return false
			}
		}
		return strings.ContainsAny(body, "0123456789")
	case "dateTime":
		return matchesLayout(s, "0000-00-00T00:00:00") && strings.Contains(s, "T")
	case "date":
		return matchesLayout(s, "0000-00-00")
	case "time":
		return matchesLayout(s, "00:00:00")
	case "anyURI":
		// Only an absolute URI is claimed. A relative reference is a string as
		// far as anyone reading the schema is concerned.
		return strings.Contains(s, "://") && !strings.ContainsAny(s, " \t\n")
	case "string":
		return true
	}
	return false
}

// matchesLayout checks a value against a digit pattern, allowing the optional
// fractional seconds and timezone the XSD temporal types permit.
func matchesLayout(s, pattern string) bool {
	if len(s) < len(pattern) {
		return false
	}
	for i, p := range []byte(pattern) {
		c := s[i]
		if p == '0' {
			if c < '0' || c > '9' {
				return false
			}
			continue
		}
		if c != p {
			return false
		}
	}
	rest := s[len(pattern):]
	if rest == "" {
		return true
	}
	if strings.HasPrefix(rest, ".") {
		rest = strings.TrimLeft(rest[1:], "0123456789")
	}
	switch {
	case rest == "", rest == "Z":
		return true
	case strings.HasPrefix(rest, "+"), strings.HasPrefix(rest, "-"):
		return matchesLayout(rest[1:], "00:00") && len(rest) == 6
	}
	return false
}
