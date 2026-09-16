package format_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/format"
)

// The fixtures are unmodified translation files taken from open source
// projects; see testdata/oss/ATTRIBUTION.md.
var fixtures = []struct {
	name     string
	format   string
	srcLang  string
	tgtLang  string
	minUnits int
}{
	{"symfony-validators.de.xlf", "xliff", "en", "de", 100},
	{"firefox-ios.de.xliff", "xliff", "en-US", "de", 1500},
	{"peertube-angular.fr-FR.xlf", "xliff", "en-US", "fr-FR", 3000},
	{"qbittorrent.de.ts", "qtts", "", "de", 2000},
	{"keepassxc.de.ts", "qtts", "", "de", 1000},
}

func load(t *testing.T, name string) ([]byte, format.Document) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "oss", name)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	doc, err := format.Parse(name, src)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return src, doc
}

func TestParseRealFiles(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			_, doc := load(t, f.name)
			if doc.Format() != f.format {
				t.Errorf("format = %q, want %q", doc.Format(), f.format)
			}
			if doc.SourceLang() != f.srcLang {
				t.Errorf("source lang = %q, want %q", doc.SourceLang(), f.srcLang)
			}
			if doc.TargetLang() != f.tgtLang {
				t.Errorf("target lang = %q, want %q", doc.TargetLang(), f.tgtLang)
			}
			units := doc.Units()
			if len(units) < f.minUnits {
				t.Errorf("parsed %d units, expected at least %d", len(units), f.minUnits)
			}
			keys := map[string]bool{}
			empty := 0
			for _, u := range units {
				if u.Key == "" {
					t.Fatal("unit with empty key")
				}
				if keys[u.Key] {
					t.Fatalf("duplicate unit key %q", u.Key)
				}
				keys[u.Key] = true
				if u.Source == "" {
					// Real exports do contain the odd placeholder entry with an
					// empty <source/>; they just must not be the bulk of the file.
					empty++
				}
			}
			if empty*100 > len(units) {
				t.Errorf("%d of %d units have an empty source", empty, len(units))
			}
			t.Logf("%s: %d units (%d with empty source)", f.name, len(units), empty)
		})
	}
}

// Parsing and writing back with no edits must reproduce the input exactly.
func TestWriteWithoutEditsIsIdentical(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			src, doc := load(t, f.name)
			out, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !bytes.Equal(src, out) {
				t.Fatalf("output differs from input at byte %d", firstDiff(src, out))
			}
		})
	}
}

// The core round-trip guarantee: after editing a subset of entries, every entry
// we did not touch is byte-for-byte what it was in the input file.
func TestUntouchedEntriesAreByteIdentical(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			src, doc := load(t, f.name)
			units := doc.Units()

			edited := map[string]bool{}
			for i := 0; i < len(units); i += 7 {
				u := units[i]
				targets := make([]string, max(1, len(u.Targets)))
				for j := range targets {
					targets[j] = "ÜBERSETZT " + itoa(j)
				}
				if !u.Plural {
					targets = targets[:1]
				}
				if err := doc.Update(u.Key, targets, format.StateApproved); err != nil {
					t.Fatalf("update %q: %v", u.Key, err)
				}
				edited[u.Key] = true
			}
			if len(edited) == 0 {
				t.Fatal("no units were edited")
			}

			out, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			outDoc, err := format.Parse(f.name, out)
			if err != nil {
				t.Fatalf("reparse: %v", err)
			}

			checked := 0
			for _, u := range units {
				if edited[u.Key] {
					continue
				}
				before, ok := doc.UnitSpan(u.Key)
				if !ok {
					t.Fatalf("no span for %q in input", u.Key)
				}
				after, ok := outDoc.UnitSpan(u.Key)
				if !ok {
					t.Fatalf("unit %q disappeared from the output", u.Key)
				}
				a := src[before.Start:before.End]
				b := out[after.Start:after.End]
				if !bytes.Equal(a, b) {
					t.Fatalf("untouched unit %q changed:\n  in:  %q\n  out: %q", u.Key, a, b)
				}
				checked++
			}
			t.Logf("%s: edited %d units, verified %d untouched units byte-identical", f.name, len(edited), checked)
		})
	}
}

// Everything outside the spans we said we would rewrite must be untouched,
// including whitespace, comments and the XML prolog.
func TestOnlyDeclaredSpansChange(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			src, doc := load(t, f.name)
			units := doc.Units()
			for i := 0; i < len(units); i += 11 {
				u := units[i]
				targets := []string{"x"}
				if u.Plural {
					targets = make([]string, len(u.Targets))
					for j := range targets {
						targets[j] = "x"
					}
				}
				if err := doc.Update(u.Key, targets, format.StateNeedsReview); err != nil {
					t.Fatal(err)
				}
			}
			changes := doc.Changes()
			out, err := doc.Bytes()
			if err != nil {
				t.Fatal(err)
			}

			inPos, outPos := 0, 0
			for _, c := range changes {
				gap := c.In.Start - inPos
				if !bytes.Equal(src[inPos:c.In.Start], out[outPos:outPos+gap]) {
					t.Fatalf("bytes outside the edited spans changed near input offset %d", inPos)
				}
				inPos = c.In.End
				outPos += gap + c.OutLen
			}
			if !bytes.Equal(src[inPos:], out[outPos:]) {
				t.Fatal("trailing bytes after the last edit changed")
			}
			t.Logf("%s: %d edited ranges, everything else identical", f.name, len(changes))
		})
	}
}

// Editing one entry must not disturb its neighbours' parsed content either.
func TestEditedEntryReadsBack(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			_, doc := load(t, f.name)
			units := doc.Units()
			target := units[len(units)/2]
			want := []string{"neue Übersetzung"}
			if target.Plural {
				want = make([]string, len(target.Targets))
				for i := range want {
					want[i] = "form " + itoa(i)
				}
			}
			if err := doc.Update(target.Key, want, format.StateApproved); err != nil {
				t.Fatal(err)
			}
			out, err := doc.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			outDoc, err := format.Parse(f.name, out)
			if err != nil {
				t.Fatalf("reparse: %v", err)
			}
			var got format.Unit
			for _, u := range outDoc.Units() {
				if u.Key == target.Key {
					got = u
				}
			}
			if got.Key == "" {
				t.Fatal("edited unit missing after reparse")
			}
			if strings.Join(got.Targets, "|") != strings.Join(want, "|") {
				t.Errorf("targets = %q, want %q", got.Targets, want)
			}
			if got.Source != target.Source {
				t.Errorf("source changed: %q -> %q", target.Source, got.Source)
			}
			// Approved entries must not export as needing review.
			switch doc.Format() {
			case "qtts":
				if got.NativeState == "unfinished" {
					t.Error("approved Qt message still marked unfinished")
				}
			case "xliff":
				if got.NativeState != "translated" {
					t.Errorf("approved XLIFF target state = %q, want translated", got.NativeState)
				}
			}
		})
	}
}

// An unreviewed translation has to come back out marked unreviewed, because
// that is how the downstream tools know not to ship it.
func TestUnreviewedExportsAsUnreviewed(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			_, doc := load(t, f.name)
			u := doc.Units()[0]
			targets := []string{"maschinell"}
			if u.Plural {
				targets = make([]string, len(u.Targets))
				for i := range targets {
					targets[i] = "maschinell"
				}
			}
			if err := doc.Update(u.Key, targets, format.StateNeedsReview); err != nil {
				t.Fatal(err)
			}
			out, _ := doc.Bytes()
			outDoc, err := format.Parse(f.name, out)
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range outDoc.Units() {
				if g.Key != u.Key {
					continue
				}
				switch doc.Format() {
				case "qtts":
					if g.NativeState != "unfinished" {
						t.Errorf("Qt type = %q, want unfinished", g.NativeState)
					}
				case "xliff":
					if g.NativeState != "needs-review-translation" {
						t.Errorf("XLIFF state = %q, want needs-review-translation", g.NativeState)
					}
					if strings.Contains(unitTag(out, outDoc, g.Key), `approved="yes"`) {
						t.Error("unreviewed XLIFF unit was marked approved")
					}
				}
			}
		})
	}
}

func TestInlinePlaceholdersSurviveAnEdit(t *testing.T) {
	src, doc := load(t, "peertube-angular.fr-FR.xlf")
	_ = src
	var withInline format.Unit
	for _, u := range doc.Units() {
		if strings.Contains(u.Source, "<x id=") && strings.Contains(u.Target(), "<x id=") {
			withInline = u
			break
		}
	}
	if withInline.Key == "" {
		t.Fatal("fixture has no unit with inline placeholders")
	}
	// Reorder the inline elements the way a real translation would.
	newTarget := withInline.Target()
	if err := doc.Update(withInline.Key, []string{newTarget}, format.StateApproved); err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	outDoc, _ := format.Parse("peertube-angular.fr-FR.xlf", out)
	for _, u := range outDoc.Units() {
		if u.Key == withInline.Key {
			if u.Target() != newTarget {
				t.Fatalf("inline markup mangled:\n  want %q\n  got  %q", newTarget, u.Target())
			}
			return
		}
	}
	t.Fatal("unit missing after round trip")
}

func TestPluralFormsRoundTrip(t *testing.T) {
	_, doc := load(t, "keepassxc.de.ts")
	plurals := 0
	var sample format.Unit
	for _, u := range doc.Units() {
		if u.Plural {
			plurals++
			if len(u.Targets) >= 2 && sample.Key == "" {
				sample = u
			}
		}
	}
	if plurals == 0 {
		t.Fatal("fixture has no numerus messages")
	}
	t.Logf("keepassxc.de.ts: %d numerus messages", plurals)
	if sample.Key == "" {
		t.Fatal("no numerus message with two forms")
	}
	want := []string{"eine Datei", "%n Dateien"}
	if err := doc.Update(sample.Key, want, format.StateApproved); err != nil {
		t.Fatal(err)
	}
	out, _ := doc.Bytes()
	outDoc, err := format.Parse("keepassxc.de.ts", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range outDoc.Units() {
		if u.Key == sample.Key {
			if !u.Plural {
				t.Fatal("numerus flag lost")
			}
			if strings.Join(u.Targets, "|") != strings.Join(want, "|") {
				t.Fatalf("forms = %q, want %q", u.Targets, want)
			}
			return
		}
	}
	t.Fatal("unit missing after round trip")
}

func TestFillsInMissingTargetElement(t *testing.T) {
	src := []byte(`<?xml version="1.0" encoding="utf-8"?>
<xliff xmlns="urn:oasis:names:tc:xliff:document:1.2" version="1.2">
  <file source-language="en" target-language="de" datatype="plaintext" original="app">
    <body>
      <trans-unit id="greeting">
        <source>Hello</source>
      </trans-unit>
    </body>
  </file>
</xliff>
`)
	doc, err := format.Parse("t.xlf", src)
	if err != nil {
		t.Fatal(err)
	}
	u := doc.Units()[0]
	if u.Target() != "" {
		t.Fatalf("expected empty target, got %q", u.Target())
	}
	if err := doc.Update(u.Key, []string{"Hallo"}, format.StateNeedsReview); err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<target state="needs-review-translation">Hallo</target>`) {
		t.Fatalf("target not inserted:\n%s", out)
	}
	if _, err := format.Parse("t.xlf", out); err != nil {
		t.Fatalf("output is not valid XLIFF: %v", err)
	}
}

func TestRejectsMalformedTranslation(t *testing.T) {
	_, doc := load(t, "symfony-validators.de.xlf")
	u := doc.Units()[0]
	if err := doc.Update(u.Key, []string{"a < b"}, format.StateApproved); err == nil {
		t.Error("expected an error for an unescaped '<'")
	}
	if err := doc.Update(u.Key, []string{"<b>bold"}, format.StateApproved); err == nil {
		t.Error("expected an error for an unclosed tag")
	}
	if err := doc.Update(u.Key, []string{`a &lt; b <x id="1"/>`}, format.StateApproved); err != nil {
		t.Errorf("valid fragment rejected: %v", err)
	}
}

// helpers

func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }

func unitTag(out []byte, doc format.Document, key string) string {
	sp, ok := doc.UnitSpan(key)
	if !ok {
		return ""
	}
	return string(out[sp.Start:sp.End])
}
