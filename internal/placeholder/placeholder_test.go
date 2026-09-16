package placeholder_test

import (
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/placeholder"
)

func names(ps []placeholder.Placeholder) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func TestExtract(t *testing.T) {
	cases := []struct {
		name    string
		dialect placeholder.Dialect
		in      string
		want    []string
	}{
		// Every one of these forms appears in the fixtures under testdata/oss.
		{"cocoa", placeholder.Generic, "Sync %@ and %@", []string{"%@", "%@"}},
		{"cocoa positional", placeholder.Generic, "Open %1$@ in %2$@", []string{"%1$@", "%2$@"}},
		{"printf", placeholder.Generic, "Downloaded %d of %s", []string{"%d", "%s"}},
		{"printf width", placeholder.Generic, "Ratio %.2f done, %-5d left", []string{"%.2f", "%-5d"}},
		{"qt numbered", placeholder.Qt, "Missing required parameters: %1", []string{"%1"}},
		{"qt localized", placeholder.Qt, "%L1 bytes of %L2", []string{"%L1", "%L2"}},
		{"qt plural", placeholder.Qt, "Timeout in %n seconds", []string{"%n"}},
		{"twig", placeholder.Generic, "This value should be {{ compared_value }} or more.", []string{"{{compared_value}}"}},
		{"icu index", placeholder.Generic, "Slide {0} of {1}", []string{"{0}", "{1}"}},
		{"python named", placeholder.Generic, "Hello %(name)s", []string{"%(name)s"}},
		{"escaped percent", placeholder.Generic, "100%% complete", nil},
		{"escaped then real", placeholder.Generic, "%% of %d", []string{"%d"}},
		{"none", placeholder.Generic, "Just words.", nil},
		{"inline element", placeholder.Generic, `Slide <x id="INTERPOLATION"/> of <x id="INTERPOLATION_1"/>`,
			[]string{`<x id="INTERPOLATION">`, `<x id="INTERPOLATION_1">`}},
		{"inline plus text", placeholder.Qt, `Quota exceeded (<x id="PH"/>, %1)`, []string{`<x id="PH">`, "%1"}},
		{"escaped markup is not a slot", placeholder.Qt, "Timeout in &lt;b&gt;%n&lt;/b&gt; seconds", []string{"%n"}},
		{"attribute text is ignored", placeholder.Generic, `a <x id="A" equiv-text="100% of"/> b`, []string{`<x id="A">`}},

		// Cases the real fixtures forced us to get right.
		{"percent followed by a word is not a slot", placeholder.Generic,
			"More than 10% of passwords are reused.", nil},
		{"qt argument beats printf width in a .ts file", placeholder.Qt,
			"%1d %2h", []string{"%1", "%2"}},
		{"printf width still wins elsewhere", placeholder.Generic,
			"%1d apples", []string{"%1d"}},
		{"icu plural exposes only the argument", placeholder.Generic,
			"{VAR_PLURAL, plural, =1 {field} other {fields}}", []string{"{VAR_PLURAL}"}},
		{"icu select exposes only the argument", placeholder.Generic,
			"{gender, select, male {he} female {she} other {they}}", []string{"{gender}"}},
		{"icu number argument", placeholder.Generic,
			"You have {count, number} items", []string{"{count}"}},
		{"stray brace is ignored", placeholder.Generic, "a { b", nil},
		{"padded braces are prose", placeholder.Generic, "found extra { or }", nil},
		{"twig spacing is normalised", placeholder.Generic,
			"{{value}} and {{ value }}", []string{"{{value}}", "{{value}}"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := names(placeholder.Extract(c.in, c.dialect))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("Extract(%q)\n got  %q\n want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		name           string
		dialect        placeholder.Dialect
		source, target string
		missing, extra []string
	}{
		{"identical", placeholder.Qt, "Save %1 now", "Jetzt %1 speichern", nil, nil},
		{"reordered is fine", placeholder.Generic, "%1$@ in %2$@", "%2$@ dans %1$@", nil, nil},
		{"dropped", placeholder.Qt, "Copy %1 to %2", "Kopiere %1", []string{"%2"}, nil},
		{"invented", placeholder.Qt, "Copy files", "Kopiere %1 Dateien", nil, []string{"%1"}},
		{"wrong specifier", placeholder.Generic, "You have %d messages", "Sie haben %s Nachrichten", []string{"%d"}, []string{"%s"}},
		{"count matters", placeholder.Generic, "%@ and %@", "%@", []string{"%@"}, nil},
		{"inline dropped", placeholder.Generic, `a <x id="PH"/> b`, "a b", []string{`<x id="PH">`}, nil},
		{"inline id changed", placeholder.Generic, `<x id="PH"/>`, `<x id="PH_1"/>`, []string{`<x id="PH">`}, []string{`<x id="PH_1">`}},

		// A real bug found in the shipped KeePassXC German translation: the
		// count placeholder is dropped and the string hardcodes the singular.
		{"keepassxc drops the count", placeholder.Qt,
			"Are you sure you want to remove %n attachment(s)?",
			"Sind Sie sicher, dass Sie einen Anhang löschen möchten?",
			[]string{"%n"}, nil},
		{"icu plural translated normally is fine", placeholder.Generic,
			"{VAR_PLURAL, plural, =1 {field} other {fields}}",
			"{VAR_PLURAL, plural, =1 {champ} other {champs}}", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := placeholder.Compare(c.source, c.target, c.dialect)
			if strings.Join(d.Missing, ",") != strings.Join(c.missing, ",") {
				t.Errorf("missing = %q, want %q", d.Missing, c.missing)
			}
			if strings.Join(d.Extra, ",") != strings.Join(c.extra, ",") {
				t.Errorf("extra = %q, want %q", d.Extra, c.extra)
			}
			if d.OK() != (len(c.missing) == 0 && len(c.extra) == 0) {
				t.Errorf("OK() = %v", d.OK())
			}
		})
	}
}
