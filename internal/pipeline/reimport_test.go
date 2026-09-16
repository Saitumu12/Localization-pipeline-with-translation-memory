package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

// tsFile builds a small Qt Linguist file. Re-importing is the normal way a
// project updates its strings: a tool such as lupdate rewrites the .ts when the
// source code changes, keeping the translations it already had.
func tsFile(messages string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE TS>
<TS version="2.1" language="de_DE" sourcelanguage="en_US">
<context>
    <name>MainWindow</name>
` + messages + `</context>
</TS>
`)
}

func message(source, translation, typeAttr string) string {
	t := "<translation"
	if typeAttr != "" {
		t += ` type="` + typeAttr + `"`
	}
	t += ">" + translation + "</translation>"
	return "    <message>\n        <source>" + source + "</source>\n        " + t + "\n    </message>\n"
}

func segmentsBySource(t *testing.T, st *store.Store, fileID int64) map[string]store.Segment {
	t.Helper()
	segs, err := st.FileSegments(context.Background(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Segment{}
	for _, s := range segs {
		out[s.SourceText] = s
	}
	return out
}

func TestReimportKeepsReviewStateForUnchangedEntries(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	first := tsFile(
		message("Save", "Speichern", "") +
			message("Delete product", "Produkt entfernen", "") +
			message("Sold %1 units", "%1 Stück verkauft", ""))

	res, err := p.Import(ctx, proj.ID, "app_de.ts", first)
	if err != nil {
		t.Fatal(err)
	}
	if res.KeptReview != 0 {
		t.Errorf("a first import has nothing to keep, got %d", res.KeptReview)
	}

	// A reviewer signs all three off.
	for _, s := range segmentsBySource(t, st, res.File.ID) {
		if _, err := p.Save(ctx, s.ID, pipeline.SaveRequest{
			Target: s.TargetText, Approve: true, Reviewer: "sai",
		}); err != nil {
			t.Fatalf("approve %q: %v", s.SourceText, err)
		}
	}

	// The app changes: one English string is reworded, one is new, and the rest
	// come back exactly as they were.
	second := tsFile(
		message("Save", "Speichern", "") +
			message("Remove product", "Produkt entfernen", "unfinished") +
			message("Sold %1 units", "%1 Stück verkauft", "") +
			message("Restock %1 units", "", "unfinished"))

	res2, err := p.Import(ctx, proj.ID, "app_de.ts", second)
	if err != nil {
		t.Fatal(err)
	}
	if res2.KeptReview != 2 {
		t.Errorf("kept review state for %d entries, want 2", res2.KeptReview)
	}

	got := segmentsBySource(t, st, res2.File.ID)

	// Unchanged entries keep their approval and their reviewer.
	for _, source := range []string{"Save", "Sold %1 units"} {
		s, ok := got[source]
		if !ok {
			t.Fatalf("%q disappeared", source)
		}
		if s.Status != "approved" {
			t.Errorf("%q status = %q, want approved", source, s.Status)
		}
		if s.ReviewedBy == nil || *s.ReviewedBy != "sai" {
			t.Errorf("%q lost its reviewer", source)
		}
	}

	// A reworded source is new work, even though the translation is carried over
	// by the tool that wrote the file. Approval does not transfer to it.
	reworded, ok := got["Remove product"]
	if !ok {
		t.Fatal("the reworded entry is missing")
	}
	if reworded.Status == "approved" {
		t.Error("a reworded source must not stay approved")
	}
	if reworded.ReviewedBy != nil {
		t.Errorf("a reworded source must not keep a reviewer, got %q", *reworded.ReviewedBy)
	}

	// The string that no longer exists is gone, and the new one is untranslated.
	if _, ok := got["Delete product"]; ok {
		t.Error("an entry the new file does not contain should not survive")
	}
	fresh, ok := got["Restock %1 units"]
	if !ok {
		t.Fatal("the new entry is missing")
	}
	if fresh.Status != "untranslated" || fresh.ReviewedBy != nil {
		t.Errorf("new entry = %q / %v, want untranslated with no reviewer", fresh.Status, fresh.ReviewedBy)
	}
}

// Editing a translation and re-importing the older file must not silently
// reinstate the old text as approved.
func TestReimportWithADifferentTranslationResetsReview(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	res, err := p.Import(ctx, proj.ID, "app_de.ts", tsFile(message("Save", "Speichern", "")))
	if err != nil {
		t.Fatal(err)
	}
	seg := segmentsBySource(t, st, res.File.ID)["Save"]
	if _, err := p.Save(ctx, seg.ID, pipeline.SaveRequest{
		Target: "Speichern", Approve: true, Reviewer: "sai",
	}); err != nil {
		t.Fatal(err)
	}

	// Somebody uploads a file where the same entry has a different translation.
	res2, err := p.Import(ctx, proj.ID, "app_de.ts", tsFile(message("Save", "Sichern", "")))
	if err != nil {
		t.Fatal(err)
	}
	if res2.KeptReview != 0 {
		t.Errorf("a changed translation is not the same work, kept %d", res2.KeptReview)
	}
	got := segmentsBySource(t, st, res2.File.ID)["Save"]
	if got.TargetText != "Sichern" {
		t.Errorf("target = %q, want the uploaded text", got.TargetText)
	}
	if got.Status == "approved" || got.ReviewedBy != nil {
		t.Error("the new translation was never reviewed and must not be approved")
	}
}

// Qt marks strings whose source has disappeared as vanished. They are history,
// not work, so they stay out of the queue and out of the export.
func TestVanishedEntriesAreIgnored(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	src := tsFile(
		message("Save", "Speichern", "") +
			message("Old feature", "Alte Funktion", "vanished"))

	res, err := p.Import(ctx, proj.ID, "app_de.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want the vanished entry skipped", res.Skipped)
	}
	if _, ok := segmentsBySource(t, st, res.File.ID)["Old feature"]; ok {
		t.Error("a vanished entry should not be in the review queue")
	}

	out, _, err := p.Export(ctx, res.File.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<translation type="vanished">Alte Funktion</translation>`) {
		t.Errorf("the vanished entry should be left exactly as it was:\n%s", out)
	}
}
