package tm_test

import (
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/store"
	"github.com/Saitumu12/localization-pipeline/internal/tm"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Save the file  ", "Save the file"},
		{"Save\n  the\tfile", "Save the file"},
		{"Save  the  file", "Save the file"},
		{"", ""},
		// Case is meaningful in a translation, so it is preserved.
		{"Save", "Save"},
	}
	for _, c := range cases {
		if got := tm.Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if tm.Normalize("Save") == tm.Normalize("save") {
		t.Error("normalisation should not fold case")
	}
}

func TestHash(t *testing.T) {
	a := tm.Hash(tm.Normalize("Save   the file"))
	b := tm.Hash(tm.Normalize("Save the file"))
	if a != b {
		t.Error("strings that normalise the same should hash the same")
	}
	if len(a) != 64 {
		t.Errorf("hash length = %d, want 64 hex characters", len(a))
	}
	if a == tm.Hash("Something else") {
		t.Error("different strings should hash differently")
	}
}

func match(kind, target string, score float64) store.Match {
	return store.Match{
		TMEntry: store.TMEntry{TargetText: target},
		Kind:    kind,
		Score:   score,
	}
}

func targets(ms []store.Match) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.TargetText
	}
	return strings.Join(out, ",")
}

func TestMerge(t *testing.T) {
	exact := []store.Match{match("exact", "Speichern", 1)}
	semantic := []store.Match{
		match("semantic", "Sichern", 0.71),
		match("semantic", "Speichern", 0.95), // the same translation, already offered
		match("semantic", "Ablegen", 0.88),
	}

	got := tm.Merge(exact, semantic, 5)
	if targets(got) != "Speichern,Ablegen,Sichern" {
		t.Errorf("merged = %q, want exact first then semantic by score", targets(got))
	}
	if got[0].Kind != "exact" {
		t.Errorf("first match kind = %q", got[0].Kind)
	}

	if got := tm.Merge(exact, semantic, 2); len(got) != 2 {
		t.Errorf("limit not applied: %d results", len(got))
	}
	if got := tm.Merge(nil, nil, 5); len(got) != 0 {
		t.Errorf("expected no matches, got %d", len(got))
	}
}
