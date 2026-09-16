// Package format parses XLIFF 1.2 and Qt .ts files and writes them back by
// splicing only the entries that changed.
package format

import (
	"fmt"
	"strings"

	"github.com/Saitumu12/localization-pipeline/internal/xmlsplice"
)

// State is the pipeline's view of a translation. Each format maps it onto its
// own vocabulary when writing, so an unreviewed string stays visibly unreviewed
// in the exported file.
type State string

const (
	StateUntranslated State = "untranslated"
	StateDraft        State = "draft"
	StateNeedsReview  State = "needs_review"
	StateApproved     State = "approved"
	StateRejected     State = "rejected"
)

// Approved reports whether a state means a human signed the translation off.
// Only this state exports as "finished"/"translated".
func (s State) Approved() bool { return s == StateApproved }

// Change describes one rewritten byte range: In is replaced by OutLen bytes.
type Change struct {
	In     xmlsplice.Span
	OutLen int
}

// Unit is one translatable entry. Source and Targets hold the raw inner XML of
// the element, not decoded text, so inline placeholder tags survive untouched.
type Unit struct {
	Key         string
	Context     string
	Source      string
	Targets     []string
	Plural      bool
	Notes       []string
	MaxWidth    int
	NativeState string
}

// Target is the first (or only) translation form.
func (u Unit) Target() string {
	if len(u.Targets) == 0 {
		return ""
	}
	return u.Targets[0]
}

// Document is a parsed localization file that can write itself back out.
type Document interface {
	Format() string
	SourceLang() string
	TargetLang() string
	Units() []Unit

	// Update queues a new translation for the unit with the given key.
	Update(key string, targets []string, st State) error

	// Bytes returns the original file with only the queued updates applied.
	Bytes() ([]byte, error)

	// Changes reports which byte ranges of the original Bytes will rewrite, and
	// how long each replacement is. Tests use it to prove nothing else moved.
	Changes() []Change

	// UnitSpan gives the byte range of a whole entry in the file it was parsed
	// from, so callers can compare entries byte for byte across a round trip.
	UnitSpan(key string) (xmlsplice.Span, bool)

	// Source returns the bytes the document was parsed from.
	Source() []byte
}

// Parse picks a parser from the file extension.
func Parse(filename string, src []byte) (Document, error) {
	switch {
	case hasExt(filename, ".ts"):
		return ParseTS(src)
	case hasExt(filename, ".xlf"), hasExt(filename, ".xliff"):
		return ParseXLIFF(src)
	default:
		return nil, fmt.Errorf("format: cannot determine format of %q (expected .xlf, .xliff or .ts)", filename)
	}
}

func hasExt(name, ext string) bool {
	return strings.HasSuffix(strings.ToLower(name), ext)
}
