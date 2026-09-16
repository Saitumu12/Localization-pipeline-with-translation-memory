package pipeline

import (
	"context"
	"fmt"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/store"
	"github.com/Saitumu12/localization-pipeline/internal/tm"
)

// AdoptResult reports what a bulk sign-off did.
type AdoptResult struct {
	Considered int `json:"considered"`
	Approved   int `json:"approved"`
	Blocked    int `json:"blocked"`
	Indexed    int `json:"indexed"`
}

// AdoptImported signs off the translations that arrived inside a file, under a
// named reviewer.
//
// A team onboarding an existing, already-shipped translation vouches for it in
// one go rather than clicking through thousands of strings. The reviewer's name
// is recorded on every row exactly as if they had approved each one, and
// segments with a blocking QA issue are skipped rather than waved through.
func (p *Pipeline) AdoptImported(ctx context.Context, fileID int64, reviewer string) (AdoptResult, error) {
	var res AdoptResult
	if reviewer == "" {
		return res, fmt.Errorf("a reviewer name is required to sign off imported translations")
	}
	f, _, err := p.Store.GetFile(ctx, fileID)
	if err != nil {
		return res, err
	}
	proj, err := p.Store.GetProject(ctx, f.ProjectID)
	if err != nil {
		return res, err
	}
	segs, err := p.Store.FileSegments(ctx, fileID)
	if err != nil {
		return res, err
	}

	type pending struct {
		seg   store.Segment
		plain string
	}
	var toIndex []pending
	indexed := map[string]bool{}

	for _, s := range segs {
		if s.Status == "untranslated" || s.Status == "approved" {
			continue
		}
		res.Considered++
		if checks.Blocking(s.Issues) {
			res.Blocked++
			continue
		}
		if _, err := p.Store.SaveSegment(ctx, s.ID, store.SegmentUpdate{
			TargetText: s.TargetText,
			Status:     "approved",
			Origin:     s.Origin,
			Reviewer:   reviewer,
			Issues:     s.Issues,
		}); err != nil {
			return res, err
		}
		res.Approved++

		// The same source and translation can appear in several contexts; the
		// memory holds one entry per distinct pair, so collapse them here rather
		// than sending the same text to the embedding service repeatedly.
		if s.FormIndex == 0 {
			plain := tm.Normalize(format.PlainText(s.SourceText))
			if plain == "" || indexed[tm.Hash(plain)+":"+s.TargetText] {
				continue
			}
			indexed[tm.Hash(plain)+":"+s.TargetText] = true
			toIndex = append(toIndex, pending{seg: s, plain: plain})
		}
	}

	if p.Embed == nil || len(toIndex) == 0 {
		return res, nil
	}

	// Embedding a few thousand strings one call at a time would be needlessly
	// slow, so they go to the service in batches.
	const batch = 64
	for start := 0; start < len(toIndex); start += batch {
		end := min(start+batch, len(toIndex))
		chunk := toIndex[start:end]

		texts := make([]string, len(chunk))
		for i, c := range chunk {
			texts[i] = c.plain
		}
		vecs, err := p.Embed.Embed(ctx, texts)
		if err != nil {
			return res, err
		}
		for i, c := range chunk {
			if err := p.Store.AddTMEntry(ctx, store.NewTMEntry{
				ProjectID:    proj.ID,
				SourceLocale: proj.SourceLocale,
				TargetLocale: proj.TargetLocale,
				SourceText:   c.plain,
				TargetText:   c.seg.TargetText,
				SourceHash:   tm.Hash(c.plain),
				Embedding:    vecs[i],
				SegmentID:    c.seg.ID,
				ApprovedBy:   reviewer,
			}); err != nil {
				return res, err
			}
			res.Indexed++
		}
	}

	// The sign-off may have created disagreements between files, so the
	// consistency check is worth rerunning once everything is in.
	if _, err := p.Recheck(ctx, fileID); err != nil {
		return res, err
	}
	return res, nil
}

// SearchMemory looks up an arbitrary source string against the memory, which is
// what the CLI uses to demonstrate the suggestions a segment would get.
func (p *Pipeline) SearchMemory(ctx context.Context, projectID int64, source string, limit int, minScore float64) ([]store.Match, error) {
	if limit <= 0 {
		limit = 5
	}
	if minScore == 0 {
		minScore = DefaultMinScore
	}
	proj, err := p.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	plain := tm.Normalize(format.PlainText(source))
	hash := tm.Hash(plain)

	exact, err := p.Store.ExactMatches(ctx, proj.ID, proj.TargetLocale, hash, limit)
	if err != nil {
		return nil, err
	}
	var semantic []store.Match
	if p.Embed != nil && plain != "" {
		vec, err := p.Embed.EmbedOne(ctx, plain)
		if err != nil {
			return nil, err
		}
		semantic, err = p.Store.SemanticMatches(ctx, proj.ID, proj.TargetLocale, vec, hash, limit, minScore)
		if err != nil {
			return nil, err
		}
	}
	return tm.Merge(exact, semantic, limit), nil
}
