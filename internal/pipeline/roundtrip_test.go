package pipeline_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

var dbFixtures = []struct {
	name   string
	locale string
}{
	{"symfony-validators.de.xlf", "de"},
	{"firefox-ios.de.xliff", "de"},
	{"peertube-angular.fr-FR.xlf", "fr-FR"},
	{"qbittorrent.de.ts", "de"},
	{"keepassxc.de.ts", "de"},
}

// A file that goes through import and straight back out again must be the same
// file, byte for byte. This is the end-to-end version of the guarantee: it
// survives the database, not just the parser.
func TestImportExportIsByteIdentical(t *testing.T) {
	for _, fx := range dbFixtures {
		t.Run(fx.name, func(t *testing.T) {
			p, st := newPipeline(t)
			ctx := context.Background()
			proj := newProject(t, st, fx.locale)

			src := fixture(t, fx.name)
			res, err := p.Import(ctx, proj.ID, fx.name, src)
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			out, name, err := p.Export(ctx, res.File.ID, false)
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if name != fx.name {
				t.Errorf("exported name = %q", name)
			}
			if !bytes.Equal(src, out) {
				t.Fatalf("export differs from import at byte %d (in %d bytes, out %d bytes)",
					firstDiff(src, out), len(src), len(out))
			}
			t.Logf("%s: %d segments imported, %d translated, %d untranslated, %d skipped",
				fx.name, res.Segments, res.Translated, res.Untranslated, res.Skipped)
		})
	}
}

// After editing a handful of entries, every entry nobody touched has to come
// back out with exactly the bytes it went in with.
func TestExportRewritesOnlyEditedEntries(t *testing.T) {
	for _, fx := range dbFixtures {
		t.Run(fx.name, func(t *testing.T) {
			p, st := newPipeline(t)
			ctx := context.Background()
			proj := newProject(t, st, fx.locale)

			src := fixture(t, fx.name)
			res, err := p.Import(ctx, proj.ID, fx.name, src)
			if err != nil {
				t.Fatal(err)
			}
			segs, err := st.FileSegments(ctx, res.File.ID)
			if err != nil {
				t.Fatal(err)
			}

			// Editing by appending to the existing translation keeps the
			// placeholders intact, so these edits are approvable.
			edited := map[string]bool{}
			for i := 0; i < len(segs); i += 250 {
				s := segs[i]
				if s.TargetText == "" {
					continue
				}
				if _, err := p.Save(ctx, s.ID, pipeline.SaveRequest{
					Target:   s.TargetText + " (geprüft)",
					Approve:  true,
					Reviewer: "sai",
				}); err != nil {
					t.Fatalf("save %d: %v", s.ID, err)
				}
				edited[s.UnitKey] = true
			}
			if len(edited) == 0 {
				t.Fatal("no segments edited")
			}

			out, _, err := p.Export(ctx, res.File.ID, false)
			if err != nil {
				t.Fatal(err)
			}

			inDoc, err := format.Parse(fx.name, src)
			if err != nil {
				t.Fatal(err)
			}
			outDoc, err := format.Parse(fx.name, out)
			if err != nil {
				t.Fatalf("exported file no longer parses: %v", err)
			}

			checked := 0
			for _, u := range inDoc.Units() {
				if edited[u.Key] {
					continue
				}
				before, ok1 := inDoc.UnitSpan(u.Key)
				after, ok2 := outDoc.UnitSpan(u.Key)
				if !ok1 || !ok2 {
					t.Fatalf("entry %q went missing", u.Key)
				}
				a := src[before.Start:before.End]
				b := out[after.Start:after.End]
				if !bytes.Equal(a, b) {
					t.Fatalf("untouched entry %q changed:\n in:  %q\n out: %q", u.Key, a, b)
				}
				checked++
			}
			t.Logf("%s: edited %d entries, %d untouched entries byte-identical", fx.name, len(edited), checked)
		})
	}
}

// A release export leaves anything that was not signed off exactly as the
// upstream file had it, and writes the approved ones with the file format's own
// "reviewed" marker.
func TestExportOnlyApproved(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	name := "qbittorrent.de.ts"
	src := fixture(t, name)
	res, err := p.Import(ctx, proj.ID, name, src)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := st.FileSegments(ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}

	// One approved edit, one unapproved edit, both on placeholder-free entries.
	var approved, draft store.Segment
	for _, s := range segs {
		if s.Status != "draft" || strings.ContainsAny(s.SourceText, "%{<") {
			continue
		}
		if approved.ID == 0 {
			approved = s
		} else if draft.ID == 0 {
			draft = s
			break
		}
	}
	if approved.ID == 0 || draft.ID == 0 {
		t.Fatal("could not find two plain entries to edit")
	}
	if _, err := p.Save(ctx, approved.ID, pipeline.SaveRequest{Target: "Genehmigt", Approve: true, Reviewer: "sai"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Save(ctx, draft.ID, pipeline.SaveRequest{Target: "Entwurf"}); err != nil {
		t.Fatal(err)
	}

	out, _, err := p.Export(ctx, res.File.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	outDoc, err := format.Parse(name, out)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range outDoc.Units() {
		switch u.Key {
		case approved.UnitKey:
			if u.Target() != "Genehmigt" {
				t.Errorf("approved entry = %q, want the approved text", u.Target())
			}
			if u.NativeState == "unfinished" {
				t.Error("approved entry exported as unfinished")
			}
		case draft.UnitKey:
			if u.Target() == "Entwurf" {
				t.Error("an unapproved draft leaked into the release export")
			}
		}
	}
}

func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
