package format

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Saitumu12/localization-pipeline/internal/xmlsplice"
)

type xliffDoc struct {
	src      []byte
	nl       string
	srcLang  string
	tgtLang  string
	units    []Unit
	slots    map[string]*slot
	unitAttr map[string]xmlsplice.Span // attributes of the <trans-unit> start tag
	unitSpan map[string]xmlsplice.Span // the whole <trans-unit> element
	updates  map[string]pendingUpdate
}

type pendingUpdate struct {
	targets []string
	state   State
}

// ParseXLIFF reads an XLIFF 1.2 document. Entries are indexed by a hash of the
// <file original> plus the trans-unit id, which is XLIFF's own notion of identity.
func ParseXLIFF(src []byte) (Document, error) {
	d := &xliffDoc{
		src:      src,
		nl:       detectNewline(src),
		slots:    map[string]*slot{},
		unitAttr: map[string]xmlsplice.Span{},
		unitSpan: map[string]xmlsplice.Span{},
		updates:  map[string]pendingUpdate{},
	}
	sc := xmlsplice.NewScanner(src)
	seen := map[string]int{}
	original := ""
	sawXliff := false

	for {
		tok, span, err := sc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xliff: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "xliff":
			sawXliff = true
		case "file":
			original = attrOf(se, "original")
			// A document can hold many <file> elements; Xcode exports one per
			// .strings file. We keep the first language pair we see and check
			// the rest agree.
			sl, tl := attrOf(se, "source-language"), attrOf(se, "target-language")
			if d.srcLang == "" {
				d.srcLang, d.tgtLang = sl, tl
			} else if sl != d.srcLang || (tl != "" && d.tgtLang != "" && tl != d.tgtLang) {
				return nil, fmt.Errorf("xliff: file elements disagree on language pair (%s->%s vs %s->%s)", d.srcLang, d.tgtLang, sl, tl)
			}
		case "trans-unit":
			if err := d.readTransUnit(sc, span, se, original, seen); err != nil {
				return nil, err
			}
		}
	}
	if !sawXliff {
		return nil, fmt.Errorf("xliff: no <xliff> root element")
	}
	return d, nil
}

func (d *xliffDoc) readTransUnit(sc *xmlsplice.Scanner, start xmlsplice.Span, se xml.StartElement, original string, seen map[string]int) error {
	id := attrOf(se, "id")
	u := Unit{
		Key:     dedupeKey(seen, unitKey("xliff", original, id)),
		Context: original,
	}
	if id != "" {
		u.Context = original + " / " + id
	}
	u.MaxWidth = xliffMaxWidth(se)
	translatable := attrOf(se, "translate") != "no"

	var sl slot
	indent := ""
	afterSource := -1

	for {
		tok, span, err := sc.Next()
		if err != nil {
			return fmt.Errorf("xliff: unit %q: %w", id, err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			// Every child is consumed whole, so the only end tag reaching here
			// closes the trans-unit itself.
			if t.Name.Local != "trans-unit" {
				return fmt.Errorf("xliff: unit %q: unexpected </%s>", id, t.Name.Local)
			}
			if !translatable {
				return nil
			}
			if !sl.exists {
				if afterSource < 0 {
					return nil // no <source>: not a translatable unit
				}
				sl.insertAt = afterSource
			}
			sl.indent = indent
			if len(u.Targets) == 0 {
				u.Targets = []string{""}
			}
			d.units = append(d.units, u)
			d.slots[u.Key] = &sl
			d.unitSpan[u.Key] = xmlsplice.Span{Start: start.Start, End: span.End}
			if attrs, ok := xmlsplice.AttrSpan(d.src, start); ok {
				d.unitAttr[u.Key] = attrs
			}
			return nil

		case xml.StartElement:
			inner, end, err := consumeElement(sc, span)
			if err != nil {
				return fmt.Errorf("xliff: unit %q: %w", id, err)
			}
			switch t.Name.Local {
			case "source":
				u.Source = string(d.src[inner.Start:inner.End])
				indent = indentBefore(d.src, span.Start)
				afterSource = end
			case "target":
				attrs, _ := xmlsplice.AttrSpan(d.src, span)
				sl = slot{
					exists: true,
					elem:   xmlsplice.Span{Start: span.Start, End: end},
					attrs:  attrs,
					inner:  inner,
				}
				u.Targets = []string{string(d.src[inner.Start:inner.End])}
				u.NativeState = attrOf(t, "state")
			case "note":
				if n := strings.TrimSpace(PlainText(string(d.src[inner.Start:inner.End]))); n != "" {
					u.Notes = append(u.Notes, n)
				}
			}
		}
	}
}

// xliffMaxWidth reads the character budget the file itself declares. XLIFF
// measures in whatever size-unit says, so only "char" is a character budget.
func xliffMaxWidth(se xml.StartElement) int {
	raw := attrOf(se, "maxwidth")
	if raw == "" {
		return 0
	}
	if u := attrOf(se, "size-unit"); u != "" && u != "char" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func (d *xliffDoc) Format() string     { return "xliff" }
func (d *xliffDoc) SourceLang() string { return d.srcLang }
func (d *xliffDoc) TargetLang() string { return d.tgtLang }
func (d *xliffDoc) Units() []Unit      { return d.units }

func (d *xliffDoc) Update(key string, targets []string, st State) error {
	if _, ok := d.slots[key]; !ok {
		return fmt.Errorf("xliff: no entry with key %q", key)
	}
	if len(targets) != 1 {
		return fmt.Errorf("xliff: entries hold exactly one target, got %d", len(targets))
	}
	if err := ValidateFragment(targets[0]); err != nil {
		return err
	}
	d.updates[key] = pendingUpdate{targets: targets, state: st}
	return nil
}

func (d *xliffDoc) edits() []xmlsplice.Edit {
	var out []xmlsplice.Edit
	for key, up := range d.updates {
		sl := d.slots[key]
		content := up.targets[0]

		attrs := ""
		if sl.exists {
			attrs = string(d.src[sl.attrs.Start:sl.attrs.End])
		}
		attrs = setAttr(attrs, "state", xliffState(up.state))

		var elem string
		if content == "" {
			elem = "<target" + attrs + "/>"
		} else {
			elem = "<target" + attrs + ">" + content + "</target>"
		}

		if sl.exists {
			out = append(out, xmlsplice.Edit{Span: sl.elem, New: []byte(elem)})
		} else {
			at := xmlsplice.Span{Start: sl.insertAt, End: sl.insertAt}
			out = append(out, xmlsplice.Edit{Span: at, New: []byte(d.nl + sl.indent + elem)})
		}

		// XLIFF 1.2 records sign-off on the trans-unit itself, so approval has
		// to be written there rather than only on the target's state.
		if span, ok := d.unitAttr[key]; ok {
			raw := string(d.src[span.Start:span.End])
			next := raw
			if up.state.Approved() {
				next = setAttr(raw, "approved", "yes")
			} else {
				next = removeAttr(raw, "approved")
			}
			if next != raw {
				out = append(out, xmlsplice.Edit{Span: span, New: []byte(next)})
			}
		}
	}
	return out
}

func (d *xliffDoc) UnitSpan(key string) (xmlsplice.Span, bool) {
	sp, ok := d.unitSpan[key]
	return sp, ok
}

func (d *xliffDoc) Source() []byte { return d.src }

func (d *xliffDoc) Bytes() ([]byte, error) { return xmlsplice.Apply(d.src, d.edits()) }

func (d *xliffDoc) Changes() []Change { return changesOf(d.edits()) }

// xliffState maps pipeline state onto the XLIFF 1.2 state vocabulary. Only an
// approved translation gets "translated"; anything a machine produced exports
// as needing review.
func xliffState(st State) string {
	switch st {
	case StateApproved:
		return "translated"
	case StateUntranslated:
		return "needs-translation"
	default:
		return "needs-review-translation"
	}
}
