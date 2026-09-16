// Package checks runs the quality gates a translation has to pass before a
// reviewer can approve it.
package checks

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/placeholder"
)

type Kind string

const (
	KindPlaceholder   Kind = "placeholder"
	KindLength        Kind = "length"
	KindTerminology   Kind = "terminology"
	KindInconsistency Kind = "inconsistency"
)

type Severity string

const (
	// An error blocks approval; a warning is shown to the reviewer.
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Issue struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

// Segment is the part of a translation unit the checks need.
type Segment struct {
	Source   string // raw inner XML of the source
	Target   string // raw inner XML of the translation
	MaxWidth int    // hard character budget declared by the file, 0 if none
	Dialect  placeholder.Dialect
}

// Term is one glossary entry: when Source appears in the English string, Target
// is the wording the translation is expected to use.
type Term struct {
	Source        string `json:"source"`
	Target        string `json:"target"`
	CaseSensitive bool   `json:"case_sensitive"`
}

// Config holds the per-project policy the checks run under.
type Config struct {
	// LengthTolerance scales the built-in expansion allowance. 1.0 is the
	// default scale, a larger number is more permissive, and 0 turns the length
	// check off for strings that declare no budget of their own.
	LengthTolerance float64 `json:"length_tolerance"`
	Glossary        []Term  `json:"-"`
}

func DefaultConfig() Config {
	return Config{LengthTolerance: 1}
}

// expansionAllowance is how much longer than the source a translation may run.
//
// One percentage across a whole file does not work: at a flat 130% the shipped
// KeePassXC German translation trips on a fifth of its strings, because short
// labels genuinely expand much more than sentences do. "Copy to clipboard" ->
// "In die Zwischenablage kopieren" is +76% and completely normal. These steps
// follow the usual localization rule of thumb that the allowance shrinks as the
// source gets longer.
func expansionAllowance(sourceLen int) float64 {
	switch {
	case sourceLen <= 10:
		return 3.0
	case sourceLen <= 20:
		return 2.2
	case sourceLen <= 30:
		return 1.8
	case sourceLen <= 50:
		return 1.6
	case sourceLen <= 70:
		return 1.4
	default:
		return 1.3
	}
}

// Run applies every per-segment check. An untranslated segment has nothing to
// check yet.
func Run(seg Segment, cfg Config) []Issue {
	if strings.TrimSpace(seg.Target) == "" {
		return nil
	}
	var issues []Issue
	issues = append(issues, checkPlaceholders(seg)...)
	issues = append(issues, checkLength(seg, cfg)...)
	issues = append(issues, checkTerminology(seg, cfg)...)
	return issues
}

// Blocking reports whether any issue is severe enough to stop approval.
func Blocking(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

func checkPlaceholders(seg Segment) []Issue {
	d := placeholder.Compare(seg.Source, seg.Target, seg.Dialect)
	if d.OK() {
		return nil
	}
	var parts []string
	if len(d.Missing) > 0 {
		parts = append(parts, "missing "+strings.Join(quoteAll(d.Missing), ", "))
	}
	if len(d.Extra) > 0 {
		parts = append(parts, "unexpected "+strings.Join(quoteAll(d.Extra), ", "))
	}
	return []Issue{{
		Kind:     KindPlaceholder,
		Severity: SeverityError,
		Message:  "placeholder mismatch: " + strings.Join(parts, "; "),
	}}
}

// checkLength compares visible characters, not bytes and not markup, because
// what overflows a button is the text a user sees.
func checkLength(seg Segment, cfg Config) []Issue {
	target := utf8.RuneCountInString(format.PlainText(seg.Target))

	if seg.MaxWidth > 0 {
		if target > seg.MaxWidth {
			return []Issue{{
				Kind:     KindLength,
				Severity: SeverityError,
				Message:  fmt.Sprintf("over the %d character budget declared for this string (%d characters)", seg.MaxWidth, target),
			}}
		}
		return nil
	}

	if cfg.LengthTolerance <= 0 {
		return nil
	}
	source := utf8.RuneCountInString(format.PlainText(seg.Source))
	budget := lengthBudget(source, cfg.LengthTolerance)
	if target > budget {
		return []Issue{{
			Kind:     KindLength,
			Severity: SeverityWarning,
			Message: fmt.Sprintf("%d characters against a %d character budget for a %d character source",
				target, budget, source),
		}}
	}
	return nil
}

// shortStringFloor is the smallest budget any string gets. A percentage is
// meaningless on a two character label: "OK" becoming "Einverstanden" is normal.
const shortStringFloor = 25

func lengthBudget(sourceLen int, tolerance float64) int {
	budget := float64(sourceLen) * expansionAllowance(sourceLen)
	if budget < shortStringFloor {
		budget = shortStringFloor
	}
	return int(budget * tolerance)
}

func checkTerminology(seg Segment, cfg Config) []Issue {
	if len(cfg.Glossary) == 0 {
		return nil
	}
	source := format.PlainText(seg.Source)
	target := format.PlainText(seg.Target)

	var issues []Issue
	for _, t := range cfg.Glossary {
		if t.Source == "" || t.Target == "" {
			continue
		}
		if !containsWord(source, t.Source, t.CaseSensitive) {
			continue
		}
		if containsWord(target, t.Target, t.CaseSensitive) {
			continue
		}
		issues = append(issues, Issue{
			Kind:     KindTerminology,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("glossary: %q should be translated as %q", t.Source, t.Target),
		})
	}
	return issues
}

// containsWord looks for term as a whole word. Go's \b is ASCII-only, which
// breaks on words like "Größe", so the boundaries are checked with unicode.
func containsWord(text, term string, caseSensitive bool) bool {
	hay, needle := text, term
	if !caseSensitive {
		hay, needle = strings.ToLower(text), strings.ToLower(term)
	}
	if needle == "" {
		return false
	}
	for from := 0; from <= len(hay)-len(needle); {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		at := from + i
		if !wordChar(lastRune(hay[:at])) {
			return true
		}
		from = at + 1
	}
	return false
}

func wordChar(r rune) bool {
	return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

func lastRune(s string) rune {
	if s == "" {
		return utf8.RuneError
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

func firstRune(s string) rune {
	if s == "" {
		return utf8.RuneError
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// Inconsistency is a source string that has been approved with more than one
// translation. It complements the glossary, which only covers terms someone
// thought to write down in advance.
type Inconsistency struct {
	Source   string   `json:"source"`
	Variants []string `json:"variants"`
}

// Consistency flags a translation that disagrees with wording already approved
// elsewhere in the same project for the identical source string.
func Consistency(target string, otherApproved []string) []Issue {
	want := strings.TrimSpace(target)
	if want == "" {
		return nil
	}
	var differing []string
	for _, o := range otherApproved {
		if o := strings.TrimSpace(o); o != "" && o != want {
			differing = append(differing, o)
		}
	}
	if len(differing) == 0 {
		return nil
	}
	sort.Strings(differing)
	shown := differing
	suffix := ""
	if len(shown) > 3 {
		shown, suffix = shown[:3], fmt.Sprintf(" and %d more", len(differing)-3)
	}
	return []Issue{{
		Kind:     KindInconsistency,
		Severity: SeverityWarning,
		Message:  "the same source string is approved elsewhere as " + strings.Join(quoteAll(shown), ", ") + suffix,
	}}
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}
