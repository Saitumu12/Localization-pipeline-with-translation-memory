// Package placeholder finds the variable slots inside a translatable string so
// a translation can be checked for keeping all of them.
package placeholder

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

type Kind string

const (
	KindPrintf Kind = "printf" // %s, %d, %@, %1$s
	KindQt     Kind = "qt"     // %1, %L2, %n
	KindBrace  Kind = "brace"  // {name}, {0}, {{ name }}, ICU arguments
	KindInline Kind = "inline" // XLIFF inline elements such as <x id="INTERPOLATION"/>
)

// Dialect says which percent syntax to prefer when a string is ambiguous.
// "%1d" is Qt's first argument followed by a literal "d" in a .ts file, but a
// printf integer with width 1 in an XLIFF file exported from C or ObjC.
type Dialect string

const (
	Generic Dialect = "generic"
	Qt      Dialect = "qt"
)

// DialectFor maps a file format onto its placeholder conventions.
func DialectFor(fileFormat string) Dialect {
	if fileFormat == "qtts" {
		return Qt
	}
	return Generic
}

// Placeholder is one variable slot. Name is the canonical form used when
// comparing a source against a translation.
type Placeholder struct {
	Name string
	Kind Kind
	Raw  string
}

// Percent syntaxes, most specific first. The two dialects differ only in
// whether Qt's numbered arguments outrank plain printf conversions.
const (
	pctEscape     = `^%%`
	pctPyNamed    = `^%\([A-Za-z_][A-Za-z0-9_]*\)[-+#0]*[0-9.]*[diouxXeEfgGaAcs@]`
	pctPositional = `^%[0-9]+\$[-+#0]*[0-9.]*(?:hh|h|ll|l|L|z|j|t|q)?[@diouxXeEfgGaAcsp]`
	pctPrintf     = `^%[-+#0]*[0-9.*]*(?:hh|h|ll|l|L|z|j|t|q)?[@diouxXeEfgGaAcsp]`
	pctQtNumber   = `^%L?[0-9]{1,2}`
	pctQtCount    = `^%n`
)

var (
	genericPercent = regexp.MustCompile(strings.Join([]string{
		pctEscape, pctPyNamed, pctPositional, pctPrintf, pctQtNumber, pctQtCount,
	}, "|"))
	qtPercent = regexp.MustCompile(strings.Join([]string{
		pctEscape, pctPositional, pctQtNumber, pctQtCount, pctPyNamed, pctPrintf,
	}, "|"))
	qtShape = regexp.MustCompile(`^%(L?[0-9]{1,2}|n)$`)
	ident   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	// ICU arguments look like {name, plural, ...} or {name, number}.
	icuArg = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.]*)\s*,`)
)

// inlineElements are the XLIFF 1.2 elements that stand in for markup or a
// variable inside a segment.
var inlineElements = map[string]bool{
	"x": true, "bx": true, "ex": true, "g": true,
	"ph": true, "bpt": true, "ept": true, "it": true,
}

// Extract returns every placeholder in a raw segment fragment. The fragment is
// inner XML, so inline elements are found by parsing and the rest by scanning
// the text they contain.
func Extract(fragment string, d Dialect) []Placeholder {
	out := extractInline(fragment)
	return append(out, extractText(stripTags(fragment), d)...)
}

func extractText(text string, d Dialect) []Placeholder {
	pct := genericPercent
	if d == Qt {
		pct = qtPercent
	}
	var out []Placeholder
	for i := 0; i < len(text); {
		switch text[i] {
		case '%':
			if m := pct.FindString(text[i:]); m != "" {
				if m != "%%" { // an escaped percent sign is not a slot
					out = append(out, Placeholder{Name: m, Kind: percentKind(m), Raw: m})
				}
				i += len(m)
				continue
			}
		case '{':
			if name, n := matchBrace(text[i:]); n > 0 {
				if name != "" {
					out = append(out, Placeholder{Name: name, Kind: KindBrace, Raw: text[i : i+n]})
				}
				i += n
				continue
			}
		}
		i++
	}
	return out
}

func percentKind(m string) Kind {
	if qtShape.MatchString(m) {
		return KindQt
	}
	return KindPrintf
}

// matchBrace reads one brace group starting at s[0] == '{'. It returns the
// canonical placeholder name (empty when the group is not a placeholder) and
// how many bytes the group occupies, or 0 if the braces are unbalanced.
//
// For an ICU message such as {count, plural, =1 {file} other {files}} only the
// argument itself is a placeholder; the nested groups are translatable text and
// must not be compared.
func matchBrace(s string) (string, int) {
	if strings.HasPrefix(s, "{{") {
		if end := strings.Index(s, "}}"); end > 0 {
			inner := strings.TrimSpace(s[2:end])
			if ident.MatchString(inner) {
				return "{{" + inner + "}}", end + 2
			}
			return "", end + 2
		}
		return "", 0
	}

	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				raw := s[1:i]
				inner := strings.TrimSpace(raw)
				switch {
				// A bare argument is written tight: "{count}". Padded braces are
				// prose, as in "found extra { or }".
				case raw == inner && (ident.MatchString(inner) || isNumber(inner)):
					return "{" + inner + "}", i + 1
				case icuArg.MatchString(inner):
					return "{" + icuArg.FindStringSubmatch(inner)[1] + "}", i + 1
				default:
					return "", i + 1
				}
			}
		}
	}
	return "", 0
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func extractInline(fragment string) []Placeholder {
	dec := xml.NewDecoder(strings.NewReader("<w>" + fragment + "</w>"))
	dec.Entity = xml.HTMLEntity
	var out []Placeholder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return out // malformed fragments are rejected when they are saved
		}
		se, ok := tok.(xml.StartElement)
		if !ok || !inlineElements[se.Name.Local] {
			continue
		}
		id := ""
		for _, a := range se.Attr {
			if a.Name.Local == "id" {
				id = a.Value
			}
		}
		name := "<" + se.Name.Local + ">"
		if id != "" {
			name = fmt.Sprintf("<%s id=%q>", se.Name.Local, id)
		}
		out = append(out, Placeholder{Name: name, Kind: KindInline, Raw: name})
	}
	return out
}

// stripTags removes markup so a text pattern cannot match inside an attribute.
// Angular writes equiv-text="get wrap(" which would otherwise look like content.
func stripTags(fragment string) string {
	dec := xml.NewDecoder(strings.NewReader("<w>" + fragment + "</w>"))
	dec.Entity = xml.HTMLEntity
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fragment
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.Write(cd)
		}
	}
	return b.String()
}

// Diff is the result of comparing a source against a translation.
type Diff struct {
	Missing []string // present in the source, absent from the translation
	Extra   []string // invented by the translation
}

func (d Diff) OK() bool { return len(d.Missing) == 0 && len(d.Extra) == 0 }

// Compare matches placeholders as a multiset, so a translation may reorder them
// but may not drop, duplicate or invent any.
func Compare(source, target string, d Dialect) Diff {
	want := count(Extract(source, d))
	got := count(Extract(target, d))

	var diff Diff
	for name, n := range want {
		if missing := n - got[name]; missing > 0 {
			diff.Missing = append(diff.Missing, repeat(name, missing)...)
		}
	}
	for name, n := range got {
		if extra := n - want[name]; extra > 0 {
			diff.Extra = append(diff.Extra, repeat(name, extra)...)
		}
	}
	sort.Strings(diff.Missing)
	sort.Strings(diff.Extra)
	return diff
}

func count(ps []Placeholder) map[string]int {
	m := make(map[string]int, len(ps))
	for _, p := range ps {
		m[p.Name]++
	}
	return m
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
