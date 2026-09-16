package format_test

import (
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/format"
)

func TestNormalizeFragment(t *testing.T) {
	cases := []struct{ in, want string }{
		// Qt accelerators: what a translator actually types.
		{"&Datei", "&amp;Datei"},
		{"E&xtras", "E&amp;xtras"},
		{"Fish & Chips", "Fish &amp; Chips"},
		// Already escaped text is left alone.
		{"&amp;Datei", "&amp;Datei"},
		{"a &lt; b", "a &lt; b"},
		{"&#246;", "&#246;"},
		{"&#xE4;", "&#xE4;"},
		{"&nbsp;", "&nbsp;"},
		// A half-written entity is a literal ampersand.
		{"&amp", "&amp;amp"},
		{"&notarealentity;", "&amp;notarealentity;"},
		// Inline markup survives untouched.
		{`Seite <x id="PH"/> von <x id="PH_1"/>`, `Seite <x id="PH"/> von <x id="PH_1"/>`},
		{"", ""},
	}
	for _, c := range cases {
		got := format.NormalizeFragment(c.in)
		if got != c.want {
			t.Errorf("NormalizeFragment(%q) = %q, want %q", c.in, got, c.want)
		}
		if err := format.ValidateFragment(got); err != nil {
			t.Errorf("NormalizeFragment(%q) produced invalid XML: %v", c.in, err)
		}
	}
}

// Normalising must not change what the reader sees.
func TestNormalizeFragmentPreservesVisibleText(t *testing.T) {
	for _, s := range []string{"&Datei", "Fish & Chips", "100% & rising", "&amp;Datei"} {
		if got, want := format.PlainText(format.NormalizeFragment(s)), format.PlainText(s); got != want {
			t.Errorf("%q: visible text became %q, want %q", s, got, want)
		}
	}
}
