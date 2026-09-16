package pipeline_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

// These four English strings are taken from the qBittorrent fixture, along with
// their shipped German translations. They are approved into the memory, and
// then looked up again through rewordings that share no distinctive vocabulary
// with the originals, so only an embedding search can connect them.
var rewordings = []struct {
	original string
	reworded string
}{
	{"Are you sure you want to quit qBittorrent?", "Do you really want to exit qBittorrent?"},
	{"Confirm when deleting torrents", "Ask for confirmation before removing torrents"},
	{"Choose folder to save exported .torrent files", "Select a directory for exported .torrent files"},
	{"Perform hostname lookup via proxy", "Resolve host names through the proxy"},
}

func TestSemanticSuggestionsForRewordedStrings(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	res, err := p.Import(ctx, proj.ID, "qbittorrent.de.ts", fixture(t, "qbittorrent.de.ts"))
	if err != nil {
		t.Fatal(err)
	}
	segs, err := st.FileSegments(ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string]store.Segment{}
	for _, s := range segs {
		if _, seen := bySource[s.SourceText]; !seen {
			bySource[s.SourceText] = s
		}
	}

	// Sign off the four originals so they enter the memory.
	want := map[string]string{}
	for _, r := range rewordings {
		seg, ok := bySource[r.original]
		if !ok {
			t.Fatalf("fixture no longer contains %q", r.original)
		}
		if seg.TargetText == "" {
			t.Fatalf("%q has no German translation in the fixture", r.original)
		}
		if _, err := p.Save(ctx, seg.ID, pipeline.SaveRequest{
			Target: seg.TargetText, Approve: true, Reviewer: "sai",
		}); err != nil {
			t.Fatalf("approve %q: %v", r.original, err)
		}
		want[r.reworded] = seg.TargetText
	}

	n, err := st.CountTMEntries(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(rewordings) {
		t.Fatalf("%d memory entries, want %d", n, len(rewordings))
	}

	// A second file whose sources are the reworded English strings.
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<xliff xmlns="urn:oasis:names:tc:xliff:document:1.2" version="1.2">
  <file source-language="en" target-language="de" datatype="plaintext" original="reworded">
    <body>
`)
	for i, r := range rewordings {
		fmt.Fprintf(&b, "      <trans-unit id=\"r%d\">\n        <source>%s</source>\n        <target/>\n      </trans-unit>\n", i, r.reworded)
	}
	b.WriteString(`      <trans-unit id="unrelated">
        <source>The rain in Spain falls mainly on the plain</source>
        <target/>
      </trans-unit>
    </body>
  </file>
</xliff>
`)
	res2, err := p.Import(ctx, proj.ID, "reworded.xlf", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	segs2, err := st.FileSegments(ctx, res2.File.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range segs2 {
		matches, err := p.Suggest(ctx, s.ID, 5, 0)
		if err != nil {
			t.Fatal(err)
		}
		expected, isReworded := want[s.SourceText]
		if !isReworded {
			if len(matches) != 0 {
				t.Errorf("unrelated source %q got %d suggestions: %+v", s.SourceText, len(matches), matches)
			}
			continue
		}
		if len(matches) == 0 {
			t.Errorf("no suggestion for reworded source %q", s.SourceText)
			continue
		}
		top := matches[0]
		if top.Kind != "semantic" {
			t.Errorf("%q: top match kind = %q, want semantic", s.SourceText, top.Kind)
		}
		if top.TargetText != expected {
			t.Errorf("%q\n  suggested: %q\n  want:      %q", s.SourceText, top.TargetText, expected)
		}
		if top.Score < 0.7 {
			t.Errorf("%q: similarity %.3f is lower than expected", s.SourceText, top.Score)
		}
		if top.ApprovedBy != "sai" {
			t.Errorf("suggestion should carry who approved it, got %q", top.ApprovedBy)
		}
		t.Logf("%.3f  %q\n        -> %q (from %q)", top.Score, s.SourceText, top.TargetText, top.SourceText)
	}
}

func TestExactMatchOutranksSemantic(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	res, err := p.Import(ctx, proj.ID, "qbittorrent.de.ts", fixture(t, "qbittorrent.de.ts"))
	if err != nil {
		t.Fatal(err)
	}
	segs, err := st.FileSegments(ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	var target store.Segment
	for _, s := range segs {
		if s.SourceText == "Are you sure you want to quit qBittorrent?" {
			target = s
			break
		}
	}
	if _, err := p.Save(ctx, target.ID, pipeline.SaveRequest{
		Target: target.TargetText, Approve: true, Reviewer: "sai",
	}); err != nil {
		t.Fatal(err)
	}

	matches, err := p.SearchMemory(ctx, proj.ID, "Are  you sure you want to quit qBittorrent?", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no match for the same string with different spacing")
	}
	if matches[0].Kind != "exact" || matches[0].Score != 1 {
		t.Errorf("top match = %+v, want an exact match", matches[0])
	}
}

// Only translations a human approved can be suggested to the next translator.
func TestOnlyApprovedTranslationsEnterTheMemory(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj, _, segs := importSmall(t, p, st)

	if _, err := p.Save(ctx, segs["quit"].ID, pipeline.SaveRequest{Target: "Anwendung beenden"}); err != nil {
		t.Fatal(err)
	}
	n, err := st.CountTMEntries(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a draft put %d entries in the memory, want 0", n)
	}

	if _, err := p.Save(ctx, segs["quit"].ID, pipeline.SaveRequest{
		Target: "Anwendung beenden", Approve: true, Reviewer: "sai",
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ = st.CountTMEntries(ctx, proj.ID); n != 1 {
		t.Fatalf("approval put %d entries in the memory, want 1", n)
	}
}

// Signing off an existing translation file loads it into the memory in bulk.
func TestAdoptImportedFillsTheMemory(t *testing.T) {
	p, st := newPipeline(t)
	ctx := context.Background()
	proj := newProject(t, st, "de")

	res, err := p.Import(ctx, proj.ID, "symfony-validators.de.xlf", fixture(t, "symfony-validators.de.xlf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.AdoptImported(ctx, res.File.ID, ""); err == nil {
		t.Error("adopting without a reviewer should fail")
	}

	adopted, err := p.AdoptImported(ctx, res.File.ID, "sai")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Approved == 0 || adopted.Indexed == 0 {
		t.Fatalf("result = %+v", adopted)
	}
	n, err := st.CountTMEntries(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != adopted.Indexed {
		t.Errorf("%d memory entries, expected %d", n, adopted.Indexed)
	}
	t.Logf("adopted %+v", adopted)

	// Every adopted segment carries the reviewer's name.
	segs, err := st.FileSegments(ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range segs {
		if s.Status == "approved" && (s.ReviewedBy == nil || *s.ReviewedBy != "sai") {
			t.Fatalf("segment %d approved without the reviewer recorded", s.ID)
		}
	}
}
