package format

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Saitumu12/localization-pipeline/internal/xmlsplice"
)

// slot records where a translation element lives in the original bytes. If the
// element is missing we remember where to insert one instead.
type slot struct {
	exists   bool
	elem     xmlsplice.Span // the whole <target>...</target>
	attrs    xmlsplice.Span // raw attribute text inside the start tag
	inner    xmlsplice.Span // content between the tags
	insertAt int            // byte offset for a new element when exists is false
	indent   string         // whitespace to put in front of an inserted element
}

// unitKey hashes a format's natural identity for an entry into a short stable
// key. Qt sources can be paragraphs long, so we do not use them as keys directly.
func unitKey(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(h[:])[:24]
}

// dedupeKey keeps keys unique when a file really does repeat an entry.
func dedupeKey(seen map[string]int, key string) string {
	n := seen[key]
	seen[key] = n + 1
	if n == 0 {
		return key
	}
	return fmt.Sprintf("%s-%d", key, n)
}

var attrPattern = regexp.MustCompile(`(^|\s)([a-zA-Z_:][-\w:.]*)\s*=\s*("[^"]*"|'[^']*')`)

// attrValue reads an attribute out of raw start-tag text.
func attrValue(raw, name string) (string, bool) {
	for _, m := range attrPattern.FindAllStringSubmatch(raw, -1) {
		if m[2] == name {
			return unescapeAttr(m[3][1 : len(m[3])-1]), true
		}
	}
	return "", false
}

// setAttr replaces an attribute's value in place, or appends it, leaving every
// other attribute's order and quoting exactly as it was.
func setAttr(raw, name, value string) string {
	for _, loc := range attrPattern.FindAllStringSubmatchIndex(raw, -1) {
		if raw[loc[4]:loc[5]] == name {
			return raw[:loc[6]+1] + escapeAttr(value) + raw[loc[7]-1:]
		}
	}
	return raw + fmt.Sprintf(` %s="%s"`, name, escapeAttr(value))
}

// removeAttr drops an attribute entirely, along with the space before it.
func removeAttr(raw, name string) string {
	for _, loc := range attrPattern.FindAllStringSubmatchIndex(raw, -1) {
		if raw[loc[4]:loc[5]] == name {
			return raw[:loc[2]] + raw[loc[1]:]
		}
	}
	return raw
}

var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func escapeAttr(s string) string { return attrEscaper.Replace(s) }

var attrUnescaper = strings.NewReplacer("&quot;", `"`, "&apos;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&")

func unescapeAttr(s string) string { return attrUnescaper.Replace(s) }

// ValidateFragment rejects a translation that would not parse back as XML.
// Translations carry inline tags such as <x id="INTERPOLATION"/>, so we accept
// markup but insist it is balanced and correctly escaped.
func ValidateFragment(s string) error {
	dec := xml.NewDecoder(strings.NewReader("<w>" + s + "</w>"))
	dec.Entity = xml.HTMLEntity
	for {
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("translation is not well-formed XML: %w", err)
		}
	}
}

// indentBefore returns the run of spaces or tabs immediately preceding off,
// used to line up an element we have to insert.
func indentBefore(src []byte, off int) string {
	i := off
	for i > 0 && (src[i-1] == ' ' || src[i-1] == '\t') {
		i--
	}
	if i > 0 && src[i-1] == '\n' {
		return string(src[i:off])
	}
	return ""
}

// PlainText strips inline markup and resolves entities, giving the text a
// translator or an embedding model should see.
func PlainText(fragment string) string {
	dec := xml.NewDecoder(strings.NewReader("<w>" + fragment + "</w>"))
	dec.Entity = xml.HTMLEntity
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			// Not parseable: fall back to the raw fragment rather than losing it.
			return fragment
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.Write(cd)
		}
	}
	return b.String()
}
