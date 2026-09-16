package format

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/Saitumu12/localization-pipeline/internal/xmlsplice"
)

type tsDoc struct {
	src     []byte
	nl      string
	srcLang string
	tgtLang string
	units   []Unit
	slots   map[string]*slot
	span    map[string]xmlsplice.Span
	plural  map[string]bool
	updates map[string]pendingUpdate
}

// ParseTS reads a Qt Linguist .ts file. Qt identifies a message by its context
// name, source text and optional disambiguating comment, so that is what we key on.
func ParseTS(src []byte) (Document, error) {
	d := &tsDoc{
		src:     src,
		nl:      detectNewline(src),
		slots:   map[string]*slot{},
		span:    map[string]xmlsplice.Span{},
		plural:  map[string]bool{},
		updates: map[string]pendingUpdate{},
	}
	sc := xmlsplice.NewScanner(src)
	seen := map[string]int{}
	context := ""
	sawTS := false

	for {
		tok, span, err := sc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("qtts: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "TS":
			sawTS = true
			d.srcLang = attrOf(se, "sourcelanguage")
			d.tgtLang = attrOf(se, "language")
		case "name":
			// <name> is the context label; it also appears inside <context>
			// only, so reading it here is enough.
			inner, _, err := consumeElement(sc, span)
			if err != nil {
				return nil, fmt.Errorf("qtts: %w", err)
			}
			context = strings.TrimSpace(PlainText(string(d.src[inner.Start:inner.End])))
		case "message":
			if err := d.readMessage(sc, span, se, context, seen); err != nil {
				return nil, err
			}
		}
	}
	if !sawTS {
		return nil, fmt.Errorf("qtts: no <TS> root element")
	}
	return d, nil
}

func (d *tsDoc) readMessage(sc *xmlsplice.Scanner, start xmlsplice.Span, se xml.StartElement, context string, seen map[string]int) error {
	u := Unit{Context: context, Plural: attrOf(se, "numerus") == "yes"}
	id := attrOf(se, "id")
	comment := ""

	var sl slot
	indent := ""
	lastChildEnd := start.End

	for {
		tok, span, err := sc.Next()
		if err != nil {
			return fmt.Errorf("qtts: message in %q: %w", context, err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local != "message" {
				return fmt.Errorf("qtts: message in %q: unexpected </%s>", context, t.Name.Local)
			}
			if u.Source == "" && id == "" {
				return nil
			}
			ident := id
			if ident == "" {
				ident = u.Source
			}
			u.Key = dedupeKey(seen, unitKey("ts", context, ident, comment))
			if !sl.exists {
				sl.insertAt = lastChildEnd
			}
			sl.indent = indent
			if len(u.Targets) == 0 {
				u.Targets = []string{""}
			}
			d.units = append(d.units, u)
			d.slots[u.Key] = &sl
			d.span[u.Key] = xmlsplice.Span{Start: start.Start, End: span.End}
			d.plural[u.Key] = u.Plural
			return nil

		case xml.StartElement:
			inner, end, err := consumeElement(sc, span)
			if err != nil {
				return fmt.Errorf("qtts: message in %q: %w", context, err)
			}
			lastChildEnd = end
			raw := string(d.src[inner.Start:inner.End])
			switch t.Name.Local {
			case "source":
				u.Source = raw
				indent = indentBefore(d.src, span.Start)
			case "comment":
				comment = raw
				if n := strings.TrimSpace(PlainText(raw)); n != "" {
					u.Notes = append(u.Notes, n)
				}
			case "extracomment", "translatorcomment":
				if n := strings.TrimSpace(PlainText(raw)); n != "" {
					u.Notes = append(u.Notes, n)
				}
			case "translation":
				attrs, _ := xmlsplice.AttrSpan(d.src, span)
				sl = slot{
					exists: true,
					elem:   xmlsplice.Span{Start: span.Start, End: end},
					attrs:  attrs,
					inner:  inner,
				}
				u.NativeState = attrOf(t, "type")
				if u.Plural {
					u.Targets = numerusForms(raw)
				} else {
					u.Targets = []string{raw}
				}
			}
		}
	}
}

// numerusForms pulls the <numerusform> children out of a plural translation.
// It slices the raw fragment rather than re-serializing, so entities and inline
// markup inside each form come back exactly as they were written.
func numerusForms(raw string) []string {
	const openTag, closeTag = "<w>", "</w>"
	wrapped := []byte(openTag + raw + closeTag)
	sc := xmlsplice.NewScanner(wrapped)
	var forms []string
	for {
		tok, span, err := sc.Next()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "numerusform" {
			continue
		}
		inner, _, err := consumeElement(sc, span)
		if err != nil {
			break
		}
		forms = append(forms, string(wrapped[inner.Start:inner.End]))
	}
	return forms
}

func (d *tsDoc) Format() string     { return "qtts" }
func (d *tsDoc) SourceLang() string { return d.srcLang }
func (d *tsDoc) TargetLang() string { return d.tgtLang }
func (d *tsDoc) Units() []Unit      { return d.units }

func (d *tsDoc) Update(key string, targets []string, st State) error {
	if _, ok := d.slots[key]; !ok {
		return fmt.Errorf("qtts: no entry with key %q", key)
	}
	if len(targets) == 0 {
		return fmt.Errorf("qtts: at least one translation form is required")
	}
	for _, t := range targets {
		if err := ValidateFragment(t); err != nil {
			return err
		}
	}
	d.updates[key] = pendingUpdate{targets: targets, state: st}
	return nil
}

func (d *tsDoc) edits() []xmlsplice.Edit {
	var out []xmlsplice.Edit
	for key, up := range d.updates {
		sl := d.slots[key]

		var content string
		if d.plural[key] {
			var b strings.Builder
			for _, f := range up.targets {
				b.WriteString("<numerusform>" + f + "</numerusform>")
			}
			content = b.String()
		} else {
			content = up.targets[0]
		}

		attrs := ""
		if sl.exists {
			attrs = string(d.src[sl.attrs.Start:sl.attrs.End])
		}
		attrs = tsTypeAttr(attrs, up.state)

		elem := "<translation" + attrs + ">" + content + "</translation>"
		if sl.exists {
			out = append(out, xmlsplice.Edit{Span: sl.elem, New: []byte(elem)})
		} else {
			at := xmlsplice.Span{Start: sl.insertAt, End: sl.insertAt}
			out = append(out, xmlsplice.Edit{Span: at, New: []byte(d.nl + sl.indent + elem)})
		}
	}
	return out
}

// tsTypeAttr keeps Qt's own review marker in sync. Qt Linguist treats a message
// with type="unfinished" as not approved, which is exactly what we want for
// anything a human has not signed off. "vanished" and "obsolete" are left alone
// because they describe the source, not the review state.
func tsTypeAttr(attrs string, st State) string {
	cur, _ := attrValue(attrs, "type")
	if cur == "vanished" || cur == "obsolete" {
		return attrs
	}
	if st.Approved() {
		return removeAttr(attrs, "type")
	}
	return setAttr(attrs, "type", "unfinished")
}

func (d *tsDoc) UnitSpan(key string) (xmlsplice.Span, bool) {
	sp, ok := d.span[key]
	return sp, ok
}

func (d *tsDoc) Source() []byte { return d.src }

func (d *tsDoc) Bytes() ([]byte, error) { return xmlsplice.Apply(d.src, d.edits()) }

func (d *tsDoc) Changes() []Change { return changesOf(d.edits()) }
