// Package xmlsplice reads XML while remembering where every token started and
// ended, so a caller can rewrite a few byte ranges and copy the rest verbatim.
package xmlsplice

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Span is a half-open byte range [Start, End) of the original document.
type Span struct {
	Start int
	End   int
}

func (s Span) Len() int { return s.End - s.Start }

// Edit replaces the bytes covered by Span with New.
type Edit struct {
	Span
	New []byte
}

// Apply copies src and substitutes the edits. Everything outside the edited
// spans is copied byte for byte, which is what makes untouched entries
// round-trip exactly.
func Apply(src []byte, edits []Edit) ([]byte, error) {
	sorted := make([]Edit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	for i, e := range sorted {
		if e.Start < 0 || e.End > len(src) || e.Start > e.End {
			return nil, fmt.Errorf("xmlsplice: edit %d has span [%d,%d) outside document of %d bytes", i, e.Start, e.End, len(src))
		}
		if i > 0 && e.Start < sorted[i-1].End {
			return nil, fmt.Errorf("xmlsplice: edits %d and %d overlap", i-1, i)
		}
	}

	size := len(src)
	for _, e := range sorted {
		size += len(e.New) - e.Len()
	}
	out := make([]byte, 0, size)

	prev := 0
	for _, e := range sorted {
		out = append(out, src[prev:e.Start]...)
		out = append(out, e.New...)
		prev = e.End
	}
	return append(out, src[prev:]...), nil
}

// Scanner walks XML tokens and reports the byte span each one occupies.
type Scanner struct {
	dec  *xml.Decoder
	prev int
}

func NewScanner(src []byte) *Scanner {
	dec := xml.NewDecoder(bytes.NewReader(src))
	// Localization files routinely carry named HTML entities that are not
	// declared in the document, so accept the HTML set rather than failing.
	dec.Entity = xml.HTMLEntity
	return &Scanner{dec: dec}
}

// Next returns the next token together with its span in the source. The token
// is copied, so it stays valid after further calls.
func (s *Scanner) Next() (xml.Token, Span, error) {
	tok, err := s.dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Span{}, io.EOF
		}
		return nil, Span{}, err
	}
	span := Span{Start: s.prev, End: int(s.dec.InputOffset())}
	s.prev = span.End
	return xml.CopyToken(tok), span, nil
}

// AttrSpan locates the raw attribute text inside a start tag, i.e. everything
// between the element name and the closing ">" or "/>". Keeping it as raw bytes
// lets us preserve attribute order, spacing and quoting we do not care about.
func AttrSpan(src []byte, tag Span) (Span, bool) {
	if tag.Start >= tag.End || src[tag.Start] != '<' {
		return Span{}, false
	}
	i := tag.Start + 1
	for i < tag.End && !isSpace(src[i]) && src[i] != '>' && src[i] != '/' {
		i++
	}
	end := tag.End - 1 // the ">"
	if end > i && src[end-1] == '/' {
		end--
	}
	if end < i {
		return Span{}, false
	}
	return Span{Start: i, End: end}, true
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
