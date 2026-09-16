package format

import (
	"bytes"
	"encoding/xml"
	"fmt"

	"github.com/Saitumu12/localization-pipeline/internal/xmlsplice"
)

// consumeElement reads to the end tag matching an already-consumed start tag.
// It returns the span of the element's inner content and the offset just past
// the element. A self-closing tag yields an empty inner span, because the
// decoder hands back a synthetic end tag at the same input position.
func consumeElement(sc *xmlsplice.Scanner, start xmlsplice.Span) (inner xmlsplice.Span, elemEnd int, err error) {
	inner.Start = start.End
	depth := 1
	for {
		tok, span, err := sc.Next()
		if err != nil {
			return inner, 0, fmt.Errorf("unterminated element: %w", err)
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				inner.End = span.Start
				return inner, span.End, nil
			}
		}
	}
}

func attrOf(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// detectNewline picks the line ending an inserted element should use.
func detectNewline(src []byte) string {
	if i := bytes.IndexByte(src, '\n'); i > 0 && src[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}
