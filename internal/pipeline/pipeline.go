// Package pipeline is the service layer: importing files, running the checks,
// suggesting translations from memory, and writing files back out.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
	"github.com/Saitumu12/localization-pipeline/internal/embed"
	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/llm"
	"github.com/Saitumu12/localization-pipeline/internal/placeholder"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

type Pipeline struct {
	Store *store.Store
	Embed *embed.Client
	LLM   *llm.Client
}

func New(st *store.Store, em *embed.Client, lm *llm.Client) *Pipeline {
	return &Pipeline{Store: st, Embed: em, LLM: lm}
}

// ImportResult summarises what an upload contained.
type ImportResult struct {
	File         store.File `json:"file"`
	Segments     int        `json:"segments"`
	Translated   int        `json:"translated"`
	Untranslated int        `json:"untranslated"`
	Skipped      int        `json:"skipped"`
}

// Import parses an uploaded file and records its entries. The uploaded bytes are
// kept verbatim; export re-splices them rather than regenerating the document.
func (p *Pipeline) Import(ctx context.Context, projectID int64, name string, data []byte) (ImportResult, error) {
	doc, err := format.Parse(name, data)
	if err != nil {
		return ImportResult{}, err
	}

	sum := sha256.Sum256(data)
	f := store.File{
		ProjectID: projectID,
		Name:      name,
		Format:    doc.Format(),
		SHA256:    hex.EncodeToString(sum[:]),
	}

	var res ImportResult
	var segs []store.NewSegment
	for _, u := range doc.Units() {
		if strings.TrimSpace(format.PlainText(u.Source)) == "" || isObsolete(u.NativeState) {
			res.Skipped++
			continue
		}
		for i, t := range u.Targets {
			status := importStatus(t, u.NativeState)
			if status == "untranslated" {
				res.Untranslated++
			} else {
				res.Translated++
			}
			segs = append(segs, store.NewSegment{
				UnitKey:    u.Key,
				FormIndex:  i,
				Context:    u.Context,
				SourceText: u.Source,
				TargetText: t,
				Status:     status,
				IsPlural:   u.Plural,
				MaxWidth:   u.MaxWidth,
				Notes:      strings.Join(u.Notes, "\n"),
			})
		}
	}
	res.Segments = len(segs)

	f, err = p.Store.InsertFile(ctx, f, data, segs)
	if err != nil {
		return res, err
	}
	res.File = f

	if _, err := p.Recheck(ctx, f.ID); err != nil {
		return res, err
	}
	return res, nil
}

// isObsolete reports entries Qt keeps around for history. They are not
// translatable work, so they stay out of the review queue and out of exports.
func isObsolete(nativeState string) bool {
	return nativeState == "vanished" || nativeState == "obsolete"
}

// importStatus decides how a translation that arrives inside a file is filed.
// Nothing is imported as approved: approval in this system means a named
// reviewer signed the string off here.
func importStatus(target, nativeState string) string {
	if strings.TrimSpace(format.PlainText(target)) == "" {
		return "untranslated"
	}
	switch nativeState {
	case "unfinished", "new", "needs-translation", "needs-review-translation",
		"needs-adaptation", "needs-review-adaptation", "needs-l10n", "needs-review-l10n":
		return "needs_review"
	default:
		return "draft"
	}
}

// Export writes the file back out. Only entries whose translation or review
// state actually differs from the uploaded file are rewritten, so everything
// else comes out byte for byte as it went in.
//
// With onlyApproved set, entries that have not been signed off are left exactly
// as the original file had them, which is how a release build is produced.
func (p *Pipeline) Export(ctx context.Context, fileID int64, onlyApproved bool) ([]byte, string, error) {
	f, original, err := p.Store.GetFile(ctx, fileID)
	if err != nil {
		return nil, "", err
	}
	doc, err := format.Parse(f.Name, original)
	if err != nil {
		return nil, "", err
	}
	segs, err := p.Store.FileSegments(ctx, fileID)
	if err != nil {
		return nil, "", err
	}

	byUnit := map[string][]store.Segment{}
	for _, s := range segs {
		byUnit[s.UnitKey] = append(byUnit[s.UnitKey], s)
	}

	for _, u := range doc.Units() {
		group, ok := byUnit[u.Key]
		if !ok || isObsolete(u.NativeState) {
			continue
		}
		state := format.State(group[0].Status)
		if onlyApproved && !state.Approved() {
			continue
		}
		targets := make([]string, len(u.Targets))
		copy(targets, u.Targets)
		for _, s := range group {
			if s.FormIndex < len(targets) {
				targets[s.FormIndex] = s.TargetText
			}
		}
		// An entry is only rewritten when its text changed or somebody worked on
		// it here. Entries that came in with the file and were never opened keep
		// their original bytes, including whatever review marker they arrived with.
		worked := group[0].Origin != "import" || group[0].ReviewedBy != nil
		if !worked && sameStrings(targets, u.Targets) {
			continue
		}
		if err := doc.Update(u.Key, targets, state); err != nil {
			return nil, "", fmt.Errorf("export %s: %w", u.Context, err)
		}
	}

	out, err := doc.Bytes()
	return out, f.Name, err
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Recheck reruns every check for a file. It is used after an import and
// whenever the project's glossary or budget rules change.
func (p *Pipeline) Recheck(ctx context.Context, fileID int64) (int, error) {
	f, _, err := p.Store.GetFile(ctx, fileID)
	if err != nil {
		return 0, err
	}
	cfg, err := p.checkConfig(ctx, f.ProjectID)
	if err != nil {
		return 0, err
	}
	segs, err := p.Store.FileSegments(ctx, fileID)
	if err != nil {
		return 0, err
	}
	// The consistency check needs to know what the rest of the project already
	// approved. Fetching that once beats asking per segment.
	approved, err := p.Store.ApprovedBySource(ctx, f.ProjectID)
	if err != nil {
		return 0, err
	}
	dialect := placeholder.DialectFor(f.Format)

	found := 0
	bySegment := make(map[int64][]checks.Issue, len(segs))
	for _, s := range segs {
		issues := checks.Run(checks.Segment{
			Source:   s.SourceText,
			Target:   s.TargetText,
			MaxWidth: s.MaxWidth,
			Dialect:  dialect,
		}, cfg)
		issues = append(issues, checks.Consistency(s.TargetText, approved[s.SourceText])...)
		if len(issues) > 0 {
			bySegment[s.ID] = issues
			found += len(issues)
		}
	}
	if err := p.Store.ReplaceFileIssues(ctx, fileID, bySegment); err != nil {
		return found, err
	}
	return found, nil
}

func (p *Pipeline) checkConfig(ctx context.Context, projectID int64) (checks.Config, error) {
	proj, err := p.Store.GetProject(ctx, projectID)
	if err != nil {
		return checks.Config{}, err
	}
	terms, err := p.Store.Glossary(ctx, projectID)
	if err != nil {
		return checks.Config{}, err
	}
	cfg := checks.Config{LengthTolerance: float64(proj.LengthTolerance)}
	for _, t := range terms {
		cfg.Glossary = append(cfg.Glossary, checks.Term{
			Source:        t.SourceTerm,
			Target:        t.TargetTerm,
			CaseSensitive: t.CaseSensitive,
		})
	}
	return cfg, nil
}
