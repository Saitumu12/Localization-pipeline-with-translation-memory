package store

import (
	"context"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
)

// TMEntry is one approved translation available for reuse.
type TMEntry struct {
	ID         int64     `json:"id"`
	SourceText string    `json:"source_text"`
	TargetText string    `json:"target_text"`
	ApprovedBy string    `json:"approved_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// Match is a suggestion with the score that produced it. Kind is "exact" when
// the normalised source text is identical, "semantic" when it came from a
// nearest-neighbour search over the embeddings.
type Match struct {
	TMEntry
	Kind  string  `json:"kind"`
	Score float64 `json:"score"`
}

// NewTMEntry is a row to add to the memory. It only ever comes from a segment a
// human approved.
type NewTMEntry struct {
	ProjectID    int64
	SourceLocale string
	TargetLocale string
	SourceText   string
	TargetText   string
	SourceHash   string
	Embedding    []float32
	SegmentID    int64
	ApprovedBy   string
}

func (s *Store) AddTMEntry(ctx context.Context, e NewTMEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tm_entries (project_id, source_locale, target_locale, source_text,
		                        target_text, source_hash, embedding, segment_id, approved_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (project_id, target_locale, source_hash, target_text)
		DO UPDATE SET embedding = EXCLUDED.embedding,
		              segment_id = EXCLUDED.segment_id,
		              approved_by = EXCLUDED.approved_by,
		              created_at = now()`,
		e.ProjectID, e.SourceLocale, e.TargetLocale, e.SourceText, e.TargetText,
		e.SourceHash, pgvector.NewVector(e.Embedding), e.SegmentID, e.ApprovedBy)
	return err
}

// ExactMatches finds memory entries whose normalised source is identical.
func (s *Store) ExactMatches(ctx context.Context, projectID int64, targetLocale, sourceHash string, limit int) ([]Match, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_text, target_text, approved_by, created_at
		FROM tm_entries
		WHERE project_id = $1 AND target_locale = $2 AND source_hash = $3
		ORDER BY created_at DESC
		LIMIT $4`, projectID, targetLocale, sourceHash, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Match{}
	for rows.Next() {
		m := Match{Kind: "exact", Score: 1}
		if err := rows.Scan(&m.ID, &m.SourceText, &m.TargetText, &m.ApprovedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SemanticMatches ranks memory entries by cosine distance to the query
// embedding. "<=>" is pgvector's cosine distance, so similarity is 1 minus it,
// and the ORDER BY is what the HNSW index on the column serves.
//
// Entries with the same normalised source are excluded because ExactMatches
// already returns those; this call is for strings that were reworded.
func (s *Store) SemanticMatches(ctx context.Context, projectID int64, targetLocale string, query []float32, excludeHash string, limit int, minScore float64) ([]Match, error) {
	vec := pgvector.NewVector(query)
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_text, target_text, approved_by, created_at,
		       1 - (embedding <=> $1) AS similarity
		FROM tm_entries
		WHERE project_id = $2 AND target_locale = $3 AND source_hash <> $4
		ORDER BY embedding <=> $1
		LIMIT $5`, vec, projectID, targetLocale, excludeHash, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Match{}
	for rows.Next() {
		m := Match{Kind: "semantic"}
		if err := rows.Scan(&m.ID, &m.SourceText, &m.TargetText, &m.ApprovedBy, &m.CreatedAt, &m.Score); err != nil {
			return nil, err
		}
		if m.Score < minScore {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CountTMEntries(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tm_entries WHERE project_id = $1`, projectID).Scan(&n)
	return n, err
}

// OtherApprovedTranslations lists the distinct approved translations this
// project already has for the same source text, ignoring one segment. It backs
// the per-segment consistency check.
func (s *Store) OtherApprovedTranslations(ctx context.Context, projectID int64, sourceText string, excludeSegment int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT s.target_text
		FROM segments s JOIN files f ON f.id = s.file_id
		WHERE f.project_id = $1
		  AND s.source_text = $2
		  AND s.status = 'approved'
		  AND s.target_text <> ''
		  AND s.id <> $3
		ORDER BY s.target_text`, projectID, sourceText, excludeSegment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ApprovedBySource returns every approved translation in a project grouped by
// its source text. Rechecking a whole file needs this once rather than asking
// about each segment in turn.
func (s *Store) ApprovedBySource(ctx context.Context, projectID int64) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.source_text, array_agg(DISTINCT s.target_text)
		FROM segments s JOIN files f ON f.id = s.file_id
		WHERE f.project_id = $1 AND s.status = 'approved' AND s.target_text <> ''
		GROUP BY s.source_text`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var source string
		var targets []string
		if err := rows.Scan(&source, &targets); err != nil {
			return nil, err
		}
		out[source] = targets
	}
	return out, rows.Err()
}

// InconsistentSources is the project-wide report: source strings that have been
// approved with more than one translation.
func (s *Store) InconsistentSources(ctx context.Context, projectID int64, limit int) ([]checks.Inconsistency, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT s.source_text, array_agg(DISTINCT s.target_text)
		FROM segments s JOIN files f ON f.id = s.file_id
		WHERE f.project_id = $1 AND s.status = 'approved' AND s.target_text <> ''
		GROUP BY s.source_text
		HAVING count(DISTINCT s.target_text) > 1
		ORDER BY s.source_text
		LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []checks.Inconsistency{}
	for rows.Next() {
		var inc checks.Inconsistency
		if err := rows.Scan(&inc.Source, &inc.Variants); err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

type GlossaryTerm struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"project_id"`
	SourceTerm    string `json:"source_term"`
	TargetTerm    string `json:"target_term"`
	CaseSensitive bool   `json:"case_sensitive"`
}

func (s *Store) AddGlossaryTerm(ctx context.Context, t GlossaryTerm) (GlossaryTerm, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO glossary_terms (project_id, source_term, target_term, case_sensitive)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (project_id, source_term) DO UPDATE
			SET target_term = EXCLUDED.target_term, case_sensitive = EXCLUDED.case_sensitive
		RETURNING id`, t.ProjectID, t.SourceTerm, t.TargetTerm, t.CaseSensitive).Scan(&t.ID)
	return t, err
}

func (s *Store) DeleteGlossaryTerm(ctx context.Context, projectID, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM glossary_terms WHERE id = $1 AND project_id = $2`, id, projectID)
	return err
}

func (s *Store) Glossary(ctx context.Context, projectID int64) ([]GlossaryTerm, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, source_term, target_term, case_sensitive
		FROM glossary_terms WHERE project_id = $1 ORDER BY source_term`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []GlossaryTerm{}
	for rows.Next() {
		var t GlossaryTerm
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.SourceTerm, &t.TargetTerm, &t.CaseSensitive); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
