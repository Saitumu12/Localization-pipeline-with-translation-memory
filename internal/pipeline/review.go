package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/llm"
	"github.com/Saitumu12/localization-pipeline/internal/placeholder"
	"github.com/Saitumu12/localization-pipeline/internal/store"
	"github.com/Saitumu12/localization-pipeline/internal/tm"
)

// SaveRequest is one edit made in the review UI.
type SaveRequest struct {
	Target   string `json:"target"`
	Approve  bool   `json:"approve"`
	Reviewer string `json:"reviewer"`
	Origin   string `json:"origin"` // "human" (default) or "memory" when a suggestion was accepted
}

// Save stores a translation a person wrote or accepted.
//
// Approval is the only route into the translation memory, and it is refused
// while the segment has a blocking issue, so a placeholder mismatch or an
// overflowing string cannot be signed off by accident.
func (p *Pipeline) Save(ctx context.Context, segmentID int64, req SaveRequest) (store.Segment, error) {
	seg, err := p.Store.GetSegment(ctx, segmentID)
	if err != nil {
		return store.Segment{}, err
	}
	// A bare "&" from a Qt accelerator is escaped rather than refused; see
	// format.NormalizeFragment.
	target := format.NormalizeFragment(req.Target)
	if err := format.ValidateFragment(target); err != nil {
		return store.Segment{}, err
	}
	f, _, err := p.Store.GetFile(ctx, seg.FileID)
	if err != nil {
		return store.Segment{}, err
	}
	proj, err := p.Store.GetProject(ctx, f.ProjectID)
	if err != nil {
		return store.Segment{}, err
	}
	cfg, err := p.checkConfig(ctx, f.ProjectID)
	if err != nil {
		return store.Segment{}, err
	}

	issues := checks.Run(checks.Segment{
		Source:   seg.SourceText,
		Target:   target,
		MaxWidth: seg.MaxWidth,
		Dialect:  placeholder.DialectFor(f.Format),
	}, cfg)
	others, err := p.Store.OtherApprovedTranslations(ctx, f.ProjectID, seg.SourceText, seg.ID)
	if err != nil {
		return store.Segment{}, err
	}
	issues = append(issues, checks.Consistency(target, others)...)

	origin := req.Origin
	if origin != "memory" {
		origin = "human"
	}
	status := "draft"
	reviewer := ""

	if req.Approve {
		if strings.TrimSpace(req.Reviewer) == "" {
			return store.Segment{}, fmt.Errorf("approving a translation requires a reviewer name")
		}
		if strings.TrimSpace(format.PlainText(target)) == "" {
			return store.Segment{}, fmt.Errorf("cannot approve an empty translation")
		}
		if checks.Blocking(issues) {
			return store.Segment{}, fmt.Errorf("cannot approve: %s", firstBlocking(issues))
		}
		status, reviewer = "approved", strings.TrimSpace(req.Reviewer)
	} else if strings.TrimSpace(format.PlainText(target)) == "" {
		status = "untranslated"
	}

	saved, err := p.Store.SaveSegment(ctx, segmentID, store.SegmentUpdate{
		TargetText: target,
		Status:     status,
		Origin:     origin,
		Reviewer:   reviewer,
		Issues:     issues,
	})
	if err != nil {
		return store.Segment{}, err
	}

	if status == "approved" {
		if err := p.indexInMemory(ctx, proj, saved, reviewer); err != nil {
			return saved, fmt.Errorf("translation saved but not added to the memory: %w", err)
		}
	}
	return saved, nil
}

func firstBlocking(issues []checks.Issue) string {
	for _, i := range issues {
		if i.Severity == checks.SeverityError {
			return i.Message
		}
	}
	return "blocking issue"
}

// indexInMemory adds an approved translation to the memory. Plural forms share
// one source string, so only the first form is indexed; otherwise the memory
// would hold several conflicting answers for the same lookup.
func (p *Pipeline) indexInMemory(ctx context.Context, proj store.Project, seg store.Segment, reviewer string) error {
	if seg.FormIndex != 0 {
		return nil
	}
	if p.Embed == nil {
		return fmt.Errorf("the embedding service is not configured")
	}
	plain := tm.Normalize(format.PlainText(seg.SourceText))
	if plain == "" {
		return nil
	}
	vec, err := p.Embed.EmbedOne(ctx, plain)
	if err != nil {
		return err
	}
	return p.Store.AddTMEntry(ctx, store.NewTMEntry{
		ProjectID:    proj.ID,
		SourceLocale: proj.SourceLocale,
		TargetLocale: proj.TargetLocale,
		SourceText:   plain,
		TargetText:   seg.TargetText,
		SourceHash:   tm.Hash(plain),
		Embedding:    vec,
		SegmentID:    seg.ID,
		ApprovedBy:   reviewer,
	})
}

// DefaultMinScore is the cosine similarity below which a memory hit is not
// worth showing. Picked by looking at real suggestions; see docs/tuning.md.
const DefaultMinScore = 0.60

// Suggest returns prior translations for a segment: exact matches on the
// normalised source first, then nearest neighbours by embedding similarity for
// strings that were reworded.
func (p *Pipeline) Suggest(ctx context.Context, segmentID int64, limit int, minScore float64) ([]store.Match, error) {
	if limit <= 0 {
		limit = 5
	}
	if minScore == 0 {
		minScore = DefaultMinScore
	}
	seg, err := p.Store.GetSegment(ctx, segmentID)
	if err != nil {
		return nil, err
	}
	f, _, err := p.Store.GetFile(ctx, seg.FileID)
	if err != nil {
		return nil, err
	}
	proj, err := p.Store.GetProject(ctx, f.ProjectID)
	if err != nil {
		return nil, err
	}

	plain := tm.Normalize(format.PlainText(seg.SourceText))
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

// PretranslateResult reports what a machine translation pass produced.
type PretranslateResult struct {
	Model       string `json:"model"`
	Requested   int    `json:"requested"`
	Translated  int    `json:"translated"`
	WithIssues  int    `json:"with_issues"`
	NeedsReview int    `json:"needs_review"`
}

// Pretranslate asks the model for a first pass over untranslated segments.
//
// Every result is written with status "needs_review" and origin "machine", and
// no reviewer is recorded. There is no argument to this function that could
// approve anything, and the segments table rejects an approved row without a
// reviewer, so a suggestion cannot reach an export as a finished translation.
func (p *Pipeline) Pretranslate(ctx context.Context, segmentIDs []int64) (PretranslateResult, error) {
	if p.LLM == nil {
		return PretranslateResult{}, fmt.Errorf("machine translation is not configured (set ANTHROPIC_API_KEY)")
	}
	res := PretranslateResult{Model: p.LLM.Model(), Requested: len(segmentIDs)}
	if len(segmentIDs) == 0 {
		return res, nil
	}
	if len(segmentIDs) > llm.BatchLimit {
		return res, fmt.Errorf("at most %d segments per request, got %d", llm.BatchLimit, len(segmentIDs))
	}

	segs := make([]store.Segment, 0, len(segmentIDs))
	for _, id := range segmentIDs {
		s, err := p.Store.GetSegment(ctx, id)
		if err != nil {
			return res, err
		}
		segs = append(segs, s)
	}

	f, _, err := p.Store.GetFile(ctx, segs[0].FileID)
	if err != nil {
		return res, err
	}
	proj, err := p.Store.GetProject(ctx, f.ProjectID)
	if err != nil {
		return res, err
	}
	cfg, err := p.checkConfig(ctx, f.ProjectID)
	if err != nil {
		return res, err
	}

	reqs := make([]llm.Request, len(segs))
	for i, s := range segs {
		reqs[i] = llm.Request{Source: s.SourceText, Note: s.Notes, MaxWidth: s.MaxWidth}
	}
	drafts, err := p.LLM.Translate(ctx, proj.SourceLocale, proj.TargetLocale, cfg.Glossary, reqs)
	if err != nil {
		return res, err
	}

	dialect := placeholder.DialectFor(f.Format)
	for i, s := range segs {
		draft := format.NormalizeFragment(drafts[i])
		// A model can still return text that is not valid XML. Recording it would
		// corrupt the export, so it is dropped and the segment is left alone.
		if err := format.ValidateFragment(draft); err != nil {
			continue
		}
		issues := checks.Run(checks.Segment{
			Source:   s.SourceText,
			Target:   draft,
			MaxWidth: s.MaxWidth,
			Dialect:  dialect,
		}, cfg)
		others, err := p.Store.OtherApprovedTranslations(ctx, f.ProjectID, s.SourceText, s.ID)
		if err != nil {
			return res, err
		}
		issues = append(issues, checks.Consistency(draft, others)...)

		if _, err := p.Store.SaveSegment(ctx, s.ID, store.SegmentUpdate{
			TargetText: draft,
			Status:     "needs_review",
			Origin:     "machine",
			Issues:     issues,
			// Reviewer is deliberately empty: a machine cannot review itself.
		}); err != nil {
			return res, err
		}
		res.Translated++
		res.NeedsReview++
		if len(issues) > 0 {
			res.WithIssues++
		}
	}
	return res, nil
}
