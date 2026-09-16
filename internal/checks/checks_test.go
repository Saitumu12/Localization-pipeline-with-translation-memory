package checks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/placeholder"
)

func kinds(issues []checks.Issue) []string {
	out := make([]string, len(issues))
	for i, is := range issues {
		out[i] = string(is.Kind) + "/" + string(is.Severity)
	}
	return out
}

func TestPlaceholderCheck(t *testing.T) {
	seg := checks.Segment{
		Source:  "Copy %1 to %2",
		Target:  "Kopiere %1",
		Dialect: placeholder.Qt,
	}
	issues := checks.Run(seg, checks.DefaultConfig())
	if len(issues) != 1 || issues[0].Kind != checks.KindPlaceholder {
		t.Fatalf("issues = %v", kinds(issues))
	}
	if issues[0].Severity != checks.SeverityError {
		t.Errorf("placeholder mismatch should block approval, got %q", issues[0].Severity)
	}
	if !strings.Contains(issues[0].Message, `"%2"`) {
		t.Errorf("message should name the missing placeholder: %s", issues[0].Message)
	}
	if !checks.Blocking(issues) {
		t.Error("Blocking should be true")
	}
}

func TestPlaceholderCheckPassesOnReorder(t *testing.T) {
	seg := checks.Segment{Source: "%1$@ in %2$@", Target: "%2$@ dans %1$@"}
	if issues := checks.Run(seg, checks.DefaultConfig()); len(issues) != 0 {
		t.Errorf("reordering is legitimate, got %v", kinds(issues))
	}
}

func TestExplicitCharacterBudget(t *testing.T) {
	cfg := checks.DefaultConfig()
	seg := checks.Segment{
		Source:   "Save",
		Target:   "Speichern unter ...",
		MaxWidth: 10,
	}
	issues := checks.Run(seg, cfg)
	if len(issues) != 1 || issues[0].Kind != checks.KindLength {
		t.Fatalf("issues = %v", kinds(issues))
	}
	if issues[0].Severity != checks.SeverityError {
		t.Errorf("a declared budget is a hard limit, got %q", issues[0].Severity)
	}
	// Exactly at the budget is fine.
	seg.Target = "Speichern"
	if issues := checks.Run(seg, cfg); len(issues) != 0 {
		t.Errorf("9 characters under a budget of 10 should pass, got %v", kinds(issues))
	}
}

func TestBudgetCountsVisibleCharacters(t *testing.T) {
	cfg := checks.DefaultConfig()
	// Markup and entities do not take up room on screen, and a multi-byte
	// character takes one column, not two.
	seg := checks.Segment{
		Source:   `Delete <x id="B"/>`,
		Target:   `L<x id="B"/>&#246;schen`,
		MaxWidth: 8,
	}
	if issues := checks.Run(seg, cfg); len(issues) != 0 {
		t.Errorf("visible length is 7, expected no issue, got %v: %v", kinds(issues), issues)
	}
	seg.MaxWidth = 6
	if issues := checks.Run(seg, cfg); len(issues) != 1 {
		t.Errorf("visible length is 7, expected an overflow at budget 6, got %v", kinds(issues))
	}
}

func TestExpansionBudget(t *testing.T) {
	cfg := checks.DefaultConfig()
	// 34 source characters, so the allowance is 160% and the budget is 54.
	source := "Please choose a destination folder"

	ok := checks.Segment{Source: source, Target: strings.Repeat("a", 54)}
	if issues := checks.Run(ok, cfg); len(issues) != 0 {
		t.Errorf("within budget, got %v", kinds(issues))
	}

	long := checks.Segment{Source: source, Target: strings.Repeat("a", 55)}
	issues := checks.Run(long, cfg)
	if len(issues) != 1 || issues[0].Kind != checks.KindLength {
		t.Fatalf("issues = %v", kinds(issues))
	}
	if issues[0].Severity != checks.SeverityWarning {
		t.Errorf("a project-wide guess is a warning, not a hard error: %q", issues[0].Severity)
	}

	// Short labels expand a long way, and that is normal, so their allowance is
	// much larger. These three are real strings from the fixtures.
	for _, c := range []struct {
		source, target string
		wantIssue      bool
	}{
		{"OK", "Einverstanden", false},
		{"Copy to clipboard", "In die Zwischenablage kopieren", false},
		{"Save", "Diese Datei irgendwo abspeichern", true},
	} {
		got := checks.Run(checks.Segment{Source: c.source, Target: c.target}, cfg)
		if (len(got) > 0) != c.wantIssue {
			t.Errorf("%q -> %q: issues %v, wantIssue %v", c.source, c.target, kinds(got), c.wantIssue)
		}
	}

	// A tolerance of zero switches the check off.
	if issues := checks.Run(long, checks.Config{}); len(issues) != 0 {
		t.Errorf("length check should be off, got %v", kinds(issues))
	}
}

func TestTerminologyCheck(t *testing.T) {
	// Only the glossary is under test here, so the length budget is off.
	cfg := checks.Config{}
	cfg.Glossary = []checks.Term{
		{Source: "bookmark", Target: "Lesezeichen"},
		{Source: "Size", Target: "Größe"},
	}

	bad := checks.Segment{Source: "Add bookmark", Target: "Favorit hinzufügen"}
	issues := checks.Run(bad, cfg)
	if len(issues) != 1 || issues[0].Kind != checks.KindTerminology {
		t.Fatalf("issues = %v", kinds(issues))
	}
	if !strings.Contains(issues[0].Message, "Lesezeichen") {
		t.Errorf("message should name the expected term: %s", issues[0].Message)
	}

	good := checks.Segment{Source: "Add bookmark", Target: "Lesezeichen hinzufügen"}
	if issues := checks.Run(good, cfg); len(issues) != 0 {
		t.Errorf("expected no issue, got %v", kinds(issues))
	}

	// Non-ASCII terms must match as whole words too.
	uml := checks.Segment{Source: "Size on disk", Target: "Größe auf der Festplatte"}
	if issues := checks.Run(uml, cfg); len(issues) != 0 {
		t.Errorf("umlaut term should match, got %v: %v", kinds(issues), issues)
	}
}

func TestTerminologyWordMatching(t *testing.T) {
	cfg := checks.Config{Glossary: []checks.Term{{Source: "tab", Target: "Tab"}}}

	// "establish" contains "tab" in the middle of a word, so it is not the term.
	seg := checks.Segment{Source: "Establish a connection", Target: "Verbindung herstellen"}
	if issues := checks.Run(seg, cfg); len(issues) != 0 {
		t.Errorf("a match inside a word should not fire, got %v", kinds(issues))
	}

	seg = checks.Segment{Source: "Close tab", Target: "Reiter schließen"}
	if issues := checks.Run(seg, cfg); len(issues) != 1 {
		t.Errorf("the term was ignored in the translation, expected an issue, got %v", kinds(issues))
	}

	// Inflected and compound forms count as using the term. German declines
	// nouns, so insisting on the exact word would flag correct translations.
	inflect := checks.Config{Glossary: []checks.Term{{Source: "torrent", Target: "Torrent"}}}
	for _, target := range []string{"Torrent entfernen", "Torrents entfernen", "Torrentdatei entfernen"} {
		seg := checks.Segment{Source: "Remove torrents", Target: target}
		if issues := checks.Run(seg, inflect); len(issues) != 0 {
			t.Errorf("%q should satisfy the term, got %v", target, kinds(issues))
		}
	}
	seg = checks.Segment{Source: "Remove torrents", Target: "Dateien entfernen"}
	if issues := checks.Run(seg, inflect); len(issues) != 1 {
		t.Errorf("the term is genuinely absent, expected an issue, got %v", kinds(issues))
	}
}

func TestUntranslatedSegmentHasNoIssues(t *testing.T) {
	cfg := checks.DefaultConfig()
	cfg.Glossary = []checks.Term{{Source: "file", Target: "Datei"}}
	seg := checks.Segment{Source: "Open file %1", Target: "  "}
	if issues := checks.Run(seg, cfg); len(issues) != 0 {
		t.Errorf("nothing to check yet, got %v", kinds(issues))
	}
}

func TestConsistency(t *testing.T) {
	if issues := checks.Consistency("Abbrechen", []string{"Abbrechen"}); len(issues) != 0 {
		t.Errorf("agreeing translations should not be flagged, got %v", issues)
	}
	if issues := checks.Consistency("Abbrechen", nil); len(issues) != 0 {
		t.Errorf("nothing to compare against, got %v", issues)
	}
	issues := checks.Consistency("Stornieren", []string{"Abbrechen", "Abbrechen"})
	if len(issues) != 1 || issues[0].Kind != checks.KindInconsistency {
		t.Fatalf("issues = %v", kinds(issues))
	}
	if issues[0].Severity != checks.SeverityWarning {
		t.Errorf("severity = %q", issues[0].Severity)
	}
	if !strings.Contains(issues[0].Message, "Abbrechen") {
		t.Errorf("message should quote the other wording: %s", issues[0].Message)
	}
}

// The checks are run over the real shipped translations in testdata/oss. These
// files are published releases, so the expected number of findings is small and
// every finding has been inspected by hand.
func TestChecksAgainstRealFiles(t *testing.T) {
	// Only the placeholder check is exercised here: the fixtures declare no
	// character budgets and carry no glossary.
	cfg := checks.Config{}

	want := map[string]int{
		"symfony-validators.de.xlf":  0,
		"firefox-ios.de.xliff":       0,
		"peertube-angular.fr-FR.xlf": 0,
		"qbittorrent.de.ts":          0,
		// Three real defects in the shipped KeePassXC German translation, where
		// the count placeholder was dropped and the singular hardcoded.
		"keepassxc.de.ts": 3,
	}

	for name, wantCount := range want {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "oss", name))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := format.Parse(name, src)
			if err != nil {
				t.Fatal(err)
			}
			dialect := placeholder.DialectFor(doc.Format())

			checked, found := 0, 0
			for _, u := range doc.Units() {
				if u.Source == "" || u.Target() == "" {
					continue
				}
				checked++
				issues := checks.Run(checks.Segment{
					Source:  u.Source,
					Target:  u.Target(),
					Dialect: dialect,
				}, cfg)
				for _, is := range issues {
					if is.Kind == checks.KindPlaceholder {
						found++
						t.Logf("%s\n  source: %s\n  target: %s\n  %s", u.Context, u.Source, u.Target(), is.Message)
					}
				}
			}
			if found != wantCount {
				t.Errorf("%d placeholder findings over %d translated strings, want %d", found, checked, wantCount)
			} else {
				t.Logf("%d translated strings checked, %d findings", checked, found)
			}
		})
	}
}
