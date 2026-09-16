package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

// importSmall loads a small hand-written XLIFF. It is a test file, not one of
// the open source fixtures, so the cases stay readable.
const smallXLIFF = `<?xml version="1.0" encoding="utf-8"?>
<xliff xmlns="urn:oasis:names:tc:xliff:document:1.2" version="1.2">
  <file source-language="en" target-language="de" datatype="plaintext" original="app">
    <body>
      <trans-unit id="copy">
        <source>Copy %1 files to %2</source>
        <target/>
      </trans-unit>
      <trans-unit id="save" maxwidth="10" size-unit="char">
        <source>Save</source>
        <target/>
      </trans-unit>
      <trans-unit id="bookmark">
        <source>Add bookmark</source>
        <target/>
      </trans-unit>
      <trans-unit id="quit">
        <source>Quit the application</source>
        <target/>
      </trans-unit>
    </body>
  </file>
</xliff>
`

func importSmall(t *testing.T, p *pipeline.Pipeline, st *store.Store) (store.Project, store.File, map[string]store.Segment) {
	t.Helper()
	ctx := context.Background()
	proj := newProject(t, st, "de")
	res, err := p.Import(ctx, proj.ID, "small.xlf", []byte(smallXLIFF))
	if err != nil {
		t.Fatal(err)
	}
	segs, err := st.FileSegments(ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]store.Segment{}
	for _, s := range segs {
		byID[strings.TrimPrefix(s.Context, "app / ")] = s
	}
	return proj, res.File, byID
}

func TestApprovalRequiresAReviewer(t *testing.T) {
	p, st := newPipeline(t)
	_, _, segs := importSmall(t, p, st)

	_, err := p.Save(context.Background(), segs["quit"].ID, pipeline.SaveRequest{
		Target:  "Anwendung beenden",
		Approve: true,
	})
	if err == nil {
		t.Fatal("approving without a reviewer should fail")
	}
	if !strings.Contains(err.Error(), "reviewer") {
		t.Errorf("error should mention the reviewer: %v", err)
	}
}

func TestCannotApproveWithABlockingIssue(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	_, _, segs := importSmall(t, p, st)

	// Dropped placeholder: a blocking error.
	if _, err := p.Save(ctx, segs["copy"].ID, pipeline.SaveRequest{
		Target: "Dateien kopieren", Approve: true, Reviewer: "sai",
	}); err == nil {
		t.Error("approving a translation that drops a placeholder should fail")
	}

	// Over a declared character budget: also blocking.
	if _, err := p.Save(ctx, segs["save"].ID, pipeline.SaveRequest{
		Target: "Speichern unter", Approve: true, Reviewer: "sai",
	}); err == nil {
		t.Error("approving a translation over its character budget should fail")
	}

	// Saving it as a draft is allowed, and records the issue for the reviewer.
	saved, err := p.Save(ctx, segs["copy"].ID, pipeline.SaveRequest{Target: "Dateien kopieren"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "draft" {
		t.Errorf("status = %q, want draft", saved.Status)
	}
	if !checks.Blocking(saved.Issues) {
		t.Errorf("expected a blocking issue, got %v", saved.Issues)
	}

	// With the placeholders kept, the same segment approves fine.
	if _, err := p.Save(ctx, segs["copy"].ID, pipeline.SaveRequest{
		Target: "%1 Dateien nach %2 kopieren", Approve: true, Reviewer: "sai",
	}); err != nil {
		t.Errorf("a correct translation should approve: %v", err)
	}
}

// The rule that nothing is approved without a named human is enforced by the
// database as well, not only by the service layer.
func TestDatabaseRejectsApprovalWithoutAReviewer(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	_, _, segs := importSmall(t, p, st)

	_, err := st.Pool().Exec(ctx,
		`UPDATE segments SET status = 'approved', target_text = 'Beenden' WHERE id = $1`,
		segs["quit"].ID)
	if err == nil {
		t.Fatal("the database should refuse an approved segment with no reviewer")
	}
	if !strings.Contains(err.Error(), "approved_needs_a_reviewer") {
		t.Errorf("expected the check constraint to fire, got: %v", err)
	}
}

func TestGlossaryIssueIsRaised(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj, file, segs := importSmall(t, p, st)

	if _, err := st.AddGlossaryTerm(ctx, store.GlossaryTerm{
		ProjectID: proj.ID, SourceTerm: "bookmark", TargetTerm: "Lesezeichen",
	}); err != nil {
		t.Fatal(err)
	}

	saved, err := p.Save(ctx, segs["bookmark"].ID, pipeline.SaveRequest{Target: "Favorit hinzufügen"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(saved.Issues, checks.KindTerminology) {
		t.Errorf("expected a terminology issue, got %v", saved.Issues)
	}

	// Using the agreed term clears it.
	saved, err = p.Save(ctx, segs["bookmark"].ID, pipeline.SaveRequest{Target: "Lesezeichen hinzufügen"})
	if err != nil {
		t.Fatal(err)
	}
	if hasIssue(saved.Issues, checks.KindTerminology) {
		t.Errorf("term was used, expected no issue, got %v", saved.Issues)
	}

	// A glossary change is picked up by rechecking the file.
	if _, err := st.AddGlossaryTerm(ctx, store.GlossaryTerm{
		ProjectID: proj.ID, SourceTerm: "bookmark", TargetTerm: "Merkliste",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Recheck(ctx, file.ID); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetSegment(ctx, segs["bookmark"].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(again.Issues, checks.KindTerminology) {
		t.Errorf("recheck should raise the new term, got %v", again.Issues)
	}
}

func TestInconsistentTerminologyAcrossFiles(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj, _, segs := importSmall(t, p, st)

	if _, err := p.Save(ctx, segs["quit"].ID, pipeline.SaveRequest{
		Target: "Anwendung beenden", Approve: true, Reviewer: "sai",
	}); err != nil {
		t.Fatal(err)
	}

	// A second file with the same English string, translated differently.
	second := strings.Replace(smallXLIFF, `original="app"`, `original="app2"`, 1)
	res, err := p.Import(ctx, proj.ID, "second.xlf", []byte(second))
	if err != nil {
		t.Fatal(err)
	}
	segs2, err := st.FileSegments(ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	var quit2 store.Segment
	for _, s := range segs2 {
		if strings.HasSuffix(s.Context, "quit") {
			quit2 = s
		}
	}

	saved, err := p.Save(ctx, quit2.ID, pipeline.SaveRequest{Target: "Programm schließen"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(saved.Issues, checks.KindInconsistency) {
		t.Fatalf("expected an inconsistency issue, got %v", saved.Issues)
	}

	report, err := st.InconsistentSources(ctx, proj.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 0 {
		t.Errorf("the second wording is not approved yet, so the report should be empty: %v", report)
	}

	if _, err := p.Save(ctx, quit2.ID, pipeline.SaveRequest{
		Target: "Programm schließen", Approve: true, Reviewer: "sai",
	}); err != nil {
		t.Fatal(err)
	}
	report, err = st.InconsistentSources(ctx, proj.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || len(report[0].Variants) != 2 {
		t.Fatalf("expected one source with two approved wordings, got %v", report)
	}
}

// A machine translation is stored as something a person still has to look at:
// status needs_review, origin machine, no reviewer.
func TestMachineTranslationGoesToReview(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	_, file, segs := importSmall(t, p, st)

	p.LLM = stubAnthropic(t, []string{
		"%1 Dateien nach %2 kopieren",
		"Sichern",
		"Lesezeichen hinzufügen",
		"Anwendung beenden",
	})

	ids := []int64{segs["copy"].ID, segs["save"].ID, segs["bookmark"].ID, segs["quit"].ID}
	res, err := p.Pretranslate(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if res.Translated != 4 || res.NeedsReview != 4 {
		t.Fatalf("result = %+v", res)
	}

	for _, id := range ids {
		s, err := st.GetSegment(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != "needs_review" {
			t.Errorf("segment %d status = %q, want needs_review", id, s.Status)
		}
		if s.Origin != "machine" {
			t.Errorf("segment %d origin = %q, want machine", id, s.Origin)
		}
		if s.ReviewedBy != nil {
			t.Errorf("segment %d has reviewer %q, want none", id, *s.ReviewedBy)
		}
	}

	// Nothing the model produced reached the translation memory.
	n, err := st.CountTMEntries(ctx, file.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d entries in the memory, want 0 before any approval", n)
	}

	// And the exported file marks them as not reviewed.
	out, _, err := p.Export(ctx, file.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := format.Parse("small.xlf", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range doc.Units() {
		if u.NativeState != "needs-review-translation" {
			t.Errorf("entry %s exported with state %q, want needs-review-translation", u.Context, u.NativeState)
		}
		if strings.Contains(string(out), `approved="yes"`) {
			t.Error("machine output was exported as approved")
		}
	}
}

// Model output is checked like any other translation, not trusted.
func TestMachineTranslationIsChecked(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	_, _, segs := importSmall(t, p, st)

	// The model drops %2 and overflows the ten character budget on "Save".
	p.LLM = stubAnthropic(t, []string{"Dateien nach %1 kopieren", "Speichern unter"})

	if _, err := p.Pretranslate(ctx, []int64{segs["copy"].ID, segs["save"].ID}); err != nil {
		t.Fatal(err)
	}

	copySeg, _ := st.GetSegment(ctx, segs["copy"].ID)
	if !hasIssue(copySeg.Issues, checks.KindPlaceholder) {
		t.Errorf("expected the dropped placeholder to be caught, got %v", copySeg.Issues)
	}
	saveSeg, _ := st.GetSegment(ctx, segs["save"].ID)
	if !hasIssue(saveSeg.Issues, checks.KindLength) {
		t.Errorf("expected the budget overflow to be caught, got %v", saveSeg.Issues)
	}

	// A reviewer cannot rubber-stamp it while the issue stands.
	if _, err := p.Save(ctx, copySeg.ID, pipeline.SaveRequest{
		Target: copySeg.TargetText, Approve: true, Reviewer: "sai",
	}); err == nil {
		t.Error("approving broken machine output should fail")
	}
}

// Text the model returns that is not valid XML is dropped rather than written,
// because storing it would corrupt the exported file.
func TestMalformedMachineOutputIsDropped(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	_, _, segs := importSmall(t, p, st)

	p.LLM = stubAnthropic(t, []string{"a < b unescaped"})
	res, err := p.Pretranslate(ctx, []int64{segs["quit"].ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Translated != 0 {
		t.Errorf("translated = %d, want 0", res.Translated)
	}
	s, _ := st.GetSegment(ctx, segs["quit"].ID)
	if s.TargetText != "" {
		t.Errorf("segment was written with %q", s.TargetText)
	}
}

func TestPretranslateWithoutAnAPIKeyFails(t *testing.T) {
	p, st := newPipeline(t)
	_, _, segs := importSmall(t, p, st)
	p.LLM = nil

	_, err := p.Pretranslate(context.Background(), []int64{segs["quit"].ID})
	if err == nil {
		t.Fatal("expected an error when machine translation is not configured")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should say what is missing: %v", err)
	}
}

func hasIssue(issues []checks.Issue, kind checks.Kind) bool {
	for _, i := range issues {
		if i.Kind == kind {
			return true
		}
	}
	return false
}

// Qt menu labels carry accelerators like "&Datei". A translator types a bare
// ampersand; the pipeline escapes it rather than refusing the translation, and
// the exported file carries the entity Qt expects.
func TestQtAcceleratorsAreAccepted(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	res, err := p.Import(ctx, proj.ID, "app_de.ts", tsFile(
		message("&amp;File", "", "unfinished")+
			message("Fish &amp; Chips", "", "unfinished")))
	if err != nil {
		t.Fatal(err)
	}
	segs := segmentsBySource(t, st, res.File.ID)

	for source, typed := range map[string]string{
		"&amp;File":        "&Datei",
		"Fish &amp; Chips": "Fisch & Pommes",
	} {
		seg, ok := segs[source]
		if !ok {
			t.Fatalf("missing segment for %q", source)
		}
		saved, err := p.Save(ctx, seg.ID, pipeline.SaveRequest{
			Target: typed, Approve: true, Reviewer: "sai",
		})
		if err != nil {
			t.Fatalf("saving %q: %v", typed, err)
		}
		if strings.Contains(saved.TargetText, "&") && !strings.Contains(saved.TargetText, "&amp;") {
			t.Errorf("stored %q, expected the ampersand escaped", saved.TargetText)
		}
		if got := format.PlainText(saved.TargetText); got != typed {
			t.Errorf("visible text = %q, want %q", got, typed)
		}
	}

	out, _, err := p.Export(ctx, res.File.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<translation>&amp;Datei</translation>") {
		t.Errorf("accelerator not exported as an entity:\n%s", out)
	}
	if _, err := format.Parse("app_de.ts", out); err != nil {
		t.Fatalf("exported file is not valid XML: %v", err)
	}
}
