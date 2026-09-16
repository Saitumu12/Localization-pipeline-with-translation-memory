package xmlsplice_test

import (
	"encoding/xml"
	"io"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/xmlsplice"
)

func TestApply(t *testing.T) {
	src := []byte("<a>one</a><b>two</b>")

	out, err := xmlsplice.Apply(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Errorf("no edits should return the input unchanged, got %q", out)
	}

	// Edits are applied in position order even when given out of order.
	out, err = xmlsplice.Apply(src, []xmlsplice.Edit{
		{Span: xmlsplice.Span{Start: 13, End: 16}, New: []byte("ZWEI")},
		{Span: xmlsplice.Span{Start: 3, End: 6}, New: []byte("1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "<a>1</a><b>ZWEI</b>" {
		t.Errorf("got %q", out)
	}

	// An empty span inserts.
	out, err = xmlsplice.Apply(src, []xmlsplice.Edit{
		{Span: xmlsplice.Span{Start: 10, End: 10}, New: []byte("<c/>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "<a>one</a><c/><b>two</b>" {
		t.Errorf("got %q", out)
	}
}

func TestApplyRejectsBadEdits(t *testing.T) {
	src := []byte("<a>one</a>")

	if _, err := xmlsplice.Apply(src, []xmlsplice.Edit{
		{Span: xmlsplice.Span{Start: 3, End: 6}, New: []byte("x")},
		{Span: xmlsplice.Span{Start: 5, End: 8}, New: []byte("y")},
	}); err == nil {
		t.Error("overlapping edits should be refused")
	}

	if _, err := xmlsplice.Apply(src, []xmlsplice.Edit{
		{Span: xmlsplice.Span{Start: 3, End: 99}, New: []byte("x")},
	}); err == nil {
		t.Error("an out of range edit should be refused")
	}

	if _, err := xmlsplice.Apply(src, []xmlsplice.Edit{
		{Span: xmlsplice.Span{Start: 6, End: 3}, New: []byte("x")},
	}); err == nil {
		t.Error("a reversed span should be refused")
	}
}

// Every token's span must line up end to end, so slicing the source by spans
// reconstructs the document exactly. That is what makes the splice safe.
func TestScannerSpansCoverTheDocument(t *testing.T) {
	src := []byte(`<?xml version="1.0"?>
<!-- a comment -->
<root attr="v">
  text &amp; more
  <empty/>
  <child xml:space="preserve"> spaced </child>
</root>`)

	sc := xmlsplice.NewScanner(src)
	var rebuilt []byte
	prev := 0
	for {
		_, span, err := sc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if span.Start != prev {
			t.Fatalf("gap between tokens: previous ended at %d, this starts at %d", prev, span.Start)
		}
		rebuilt = append(rebuilt, src[span.Start:span.End]...)
		prev = span.End
	}
	if string(rebuilt) != string(src) {
		t.Errorf("token spans do not reconstruct the document:\n%q", rebuilt)
	}
}

func TestAttrSpan(t *testing.T) {
	cases := []struct {
		tag  string
		want string
	}{
		{`<target>`, ""},
		{`<target/>`, ""},
		{`<target />`, " "},
		{`<target state="translated">`, ` state="translated"`},
		{`<target state="new" xml:space="preserve"/>`, ` state="new" xml:space="preserve"`},
	}
	for _, c := range cases {
		src := []byte(c.tag)
		span, ok := xmlsplice.AttrSpan(src, xmlsplice.Span{Start: 0, End: len(src)})
		if !ok {
			t.Errorf("%s: no attribute span found", c.tag)
			continue
		}
		got := string(src[span.Start:span.End])
		if got != c.want {
			t.Errorf("%s: attributes = %q, want %q", c.tag, got, c.want)
		}
		// Rebuilding from the name plus the raw attributes must give the tag back.
		suffix := ">"
		if c.tag[len(c.tag)-2] == '/' {
			suffix = "/>"
		}
		if rebuilt := "<target" + got + suffix; rebuilt != c.tag {
			t.Errorf("rebuilt %q, want %q", rebuilt, c.tag)
		}
	}
}

// Localization files often carry named HTML entities that no DTD declares.
func TestScannerAcceptsHTMLEntities(t *testing.T) {
	sc := xmlsplice.NewScanner([]byte("<a>ten&nbsp;items &mdash; done</a>"))
	seen := false
	for {
		tok, _, err := sc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decoder rejected a named entity: %v", err)
		}
		if _, ok := tok.(xml.CharData); ok {
			seen = true
		}
	}
	if !seen {
		t.Error("expected character data")
	}
}
