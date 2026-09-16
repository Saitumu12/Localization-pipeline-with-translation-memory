package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
)

type File struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"project_id"`
	Name       string    `json:"name"`
	Format     string    `json:"format"`
	SHA256     string    `json:"sha256"`
	ImportedAt time.Time `json:"imported_at"`
}

type Segment struct {
	ID         int64  `json:"id"`
	FileID     int64  `json:"file_id"`
	UnitKey    string `json:"unit_key"`
	FormIndex  int    `json:"form_index"`
	Context    string `json:"context"`
	SourceText string `json:"source_text"`
	TargetText string `json:"target_text"`
	Status     string `json:"status"`
	Origin     string `json:"origin"`
	IsPlural   bool   `json:"is_plural"`
	MaxWidth   int    `json:"max_width"`
	Notes      string `json:"notes"`

	ReviewedBy *string    `json:"reviewed_by"`
	ReviewedAt *time.Time `json:"reviewed_at"`
	UpdatedAt  time.Time  `json:"updated_at"`

	Issues []checks.Issue `json:"issues"`
}

// NewSegment is one row to write during an import.
type NewSegment struct {
	UnitKey    string
	FormIndex  int
	Context    string
	SourceText string
	TargetText string
	Status     string
	IsPlural   bool
	MaxWidth   int
	Notes      string
}

// InsertFile stores an uploaded file and all of its segments in one transaction,
// so a half-imported file can never be left behind.
func (s *Store) InsertFile(ctx context.Context, f File, original []byte, segs []NewSegment) (File, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return f, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	err = tx.QueryRow(ctx, `
		INSERT INTO files (project_id, name, format, original, sha256)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, name) DO UPDATE
			SET original = EXCLUDED.original, format = EXCLUDED.format,
			    sha256 = EXCLUDED.sha256, imported_at = now()
		RETURNING id, imported_at`,
		f.ProjectID, f.Name, f.Format, original, f.SHA256,
	).Scan(&f.ID, &f.ImportedAt)
	if err != nil {
		return f, fmt.Errorf("store: insert file: %w", err)
	}

	// A re-import replaces the segment set; translations are rebuilt from the
	// file's own targets, which is what the uploaded bytes say is true.
	if _, err := tx.Exec(ctx, `DELETE FROM segments WHERE file_id = $1`, f.ID); err != nil {
		return f, err
	}

	rows := make([][]any, 0, len(segs))
	for _, sg := range segs {
		rows = append(rows, []any{
			f.ID, sg.UnitKey, sg.FormIndex, sg.Context, sg.SourceText,
			sg.TargetText, sg.Status, "import", sg.IsPlural, sg.MaxWidth, sg.Notes,
		})
	}
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"segments"},
		[]string{"file_id", "unit_key", "form_index", "context", "source_text",
			"target_text", "status", "origin", "is_plural", "max_width", "notes"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return f, fmt.Errorf("store: insert segments: %w", err)
	}

	return f, tx.Commit(ctx)
}

func (s *Store) ListFiles(ctx context.Context, projectID int64) ([]File, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, name, format, sha256, imported_at
		FROM files WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []File{}
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Name, &f.Format, &f.SHA256, &f.ImportedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) GetFile(ctx context.Context, id int64) (File, []byte, error) {
	var f File
	var original []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, project_id, name, format, sha256, imported_at, original
		FROM files WHERE id = $1`, id,
	).Scan(&f.ID, &f.ProjectID, &f.Name, &f.Format, &f.SHA256, &f.ImportedAt, &original)
	return f, original, err
}

// SegmentFilter narrows the review queue.
type SegmentFilter struct {
	Status    string // "" for any
	OnlyIssue bool   // only segments with at least one open QA issue
	Search    string // substring of the source text
	Limit     int
	Offset    int
}

func (s *Store) ListSegments(ctx context.Context, fileID int64, f SegmentFilter) ([]Segment, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := `WHERE s.file_id = $1
		AND ($2 = '' OR s.status::text = $2)
		AND ($3 = '' OR s.source_text ILIKE '%' || $3 || '%')
		AND (NOT $4 OR EXISTS (SELECT 1 FROM qa_issues q WHERE q.segment_id = s.id))`

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM segments s `+where, fileID, f.Status, f.Search, f.OnlyIssue,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.file_id, s.unit_key, s.form_index, s.context, s.source_text,
		       s.target_text, s.status::text, s.origin::text, s.is_plural, s.max_width,
		       s.notes, s.reviewed_by, s.reviewed_at, s.updated_at
		FROM segments s `+where+`
		ORDER BY s.id
		LIMIT $5 OFFSET $6`, fileID, f.Status, f.Search, f.OnlyIssue, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []Segment{}
	ids := []int64{}
	for rows.Next() {
		var sg Segment
		if err := rows.Scan(&sg.ID, &sg.FileID, &sg.UnitKey, &sg.FormIndex, &sg.Context,
			&sg.SourceText, &sg.TargetText, &sg.Status, &sg.Origin, &sg.IsPlural,
			&sg.MaxWidth, &sg.Notes, &sg.ReviewedBy, &sg.ReviewedAt, &sg.UpdatedAt); err != nil {
			return nil, 0, err
		}
		sg.Issues = []checks.Issue{}
		out = append(out, sg)
		ids = append(ids, sg.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	issues, err := s.issuesFor(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		if is, ok := issues[out[i].ID]; ok {
			out[i].Issues = is
		}
	}
	return out, total, nil
}

func (s *Store) issuesFor(ctx context.Context, ids []int64) (map[int64][]checks.Issue, error) {
	out := map[int64][]checks.Issue{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT segment_id, kind, severity, message FROM qa_issues WHERE segment_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var is checks.Issue
		if err := rows.Scan(&id, &is.Kind, &is.Severity, &is.Message); err != nil {
			return nil, err
		}
		out[id] = append(out[id], is)
	}
	return out, rows.Err()
}

func (s *Store) GetSegment(ctx context.Context, id int64) (Segment, error) {
	var sg Segment
	err := s.pool.QueryRow(ctx, `
		SELECT id, file_id, unit_key, form_index, context, source_text, target_text,
		       status::text, origin::text, is_plural, max_width, notes,
		       reviewed_by, reviewed_at, updated_at
		FROM segments WHERE id = $1`, id,
	).Scan(&sg.ID, &sg.FileID, &sg.UnitKey, &sg.FormIndex, &sg.Context, &sg.SourceText,
		&sg.TargetText, &sg.Status, &sg.Origin, &sg.IsPlural, &sg.MaxWidth, &sg.Notes,
		&sg.ReviewedBy, &sg.ReviewedAt, &sg.UpdatedAt)
	if err != nil {
		return sg, err
	}
	issues, err := s.issuesFor(ctx, []int64{id})
	if err != nil {
		return sg, err
	}
	sg.Issues = issues[id]
	if sg.Issues == nil {
		sg.Issues = []checks.Issue{}
	}
	return sg, nil
}

// SegmentUpdate is a write to one segment, with the QA issues recomputed for it.
type SegmentUpdate struct {
	TargetText string
	Status     string
	Origin     string
	Reviewer   string // required when Status is "approved"
	Issues     []checks.Issue
}

// SaveSegment writes the translation and replaces that segment's QA issues.
// The reviewer columns are only ever set here, from an explicit argument; no
// other code path can stamp a segment as reviewed.
func (s *Store) SaveSegment(ctx context.Context, id int64, up SegmentUpdate) (Segment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Segment{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var reviewer *string
	var reviewedAt *time.Time
	if up.Status == "approved" {
		if up.Reviewer == "" {
			return Segment{}, fmt.Errorf("store: approving a segment requires a reviewer")
		}
		now := time.Now()
		reviewer, reviewedAt = &up.Reviewer, &now
	}

	_, err = tx.Exec(ctx, `
		UPDATE segments
		SET target_text = $2, status = $3::segment_status, origin = $4::segment_origin,
		    reviewed_by = $5, reviewed_at = $6, updated_at = now()
		WHERE id = $1`, id, up.TargetText, up.Status, up.Origin, reviewer, reviewedAt)
	if err != nil {
		return Segment{}, fmt.Errorf("store: save segment: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM qa_issues WHERE segment_id = $1`, id); err != nil {
		return Segment{}, err
	}
	for _, is := range up.Issues {
		if _, err := tx.Exec(ctx,
			`INSERT INTO qa_issues (segment_id, kind, severity, message) VALUES ($1, $2, $3, $4)`,
			id, is.Kind, is.Severity, is.Message); err != nil {
			return Segment{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Segment{}, err
	}
	return s.GetSegment(ctx, id)
}

// ReplaceFileIssues rewrites the QA findings for a whole file in one
// transaction. Doing it per segment meant thousands of round trips on a file
// the size of the PeerTube fixture.
func (s *Store) ReplaceFileIssues(ctx context.Context, fileID int64, issues map[int64][]checks.Issue) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`DELETE FROM qa_issues WHERE segment_id IN (SELECT id FROM segments WHERE file_id = $1)`,
		fileID); err != nil {
		return err
	}

	var rows [][]any
	for segmentID, list := range issues {
		for _, is := range list {
			rows = append(rows, []any{segmentID, is.Kind, is.Severity, is.Message})
		}
	}
	if len(rows) > 0 {
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"qa_issues"},
			[]string{"segment_id", "kind", "severity", "message"},
			pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// FileSegments returns every segment of a file in export order.
func (s *Store) FileSegments(ctx context.Context, fileID int64) ([]Segment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, file_id, unit_key, form_index, context, source_text, target_text,
		       status::text, origin::text, is_plural, max_width, notes,
		       reviewed_by, reviewed_at, updated_at
		FROM segments WHERE file_id = $1 ORDER BY unit_key, form_index`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Segment{}
	for rows.Next() {
		var sg Segment
		if err := rows.Scan(&sg.ID, &sg.FileID, &sg.UnitKey, &sg.FormIndex, &sg.Context,
			&sg.SourceText, &sg.TargetText, &sg.Status, &sg.Origin, &sg.IsPlural,
			&sg.MaxWidth, &sg.Notes, &sg.ReviewedBy, &sg.ReviewedAt, &sg.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// FileStats is the progress summary shown next to each file.
type FileStats struct {
	Total        int `json:"total"`
	Approved     int `json:"approved"`
	NeedsReview  int `json:"needs_review"`
	Draft        int `json:"draft"`
	Untranslated int `json:"untranslated"`
	Rejected     int `json:"rejected"`
	Errors       int `json:"errors"`
	Warnings     int `json:"warnings"`
}

func (s *Store) StatsFor(ctx context.Context, fileID int64) (FileStats, error) {
	var st FileStats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'approved'),
		       count(*) FILTER (WHERE status = 'needs_review'),
		       count(*) FILTER (WHERE status = 'draft'),
		       count(*) FILTER (WHERE status = 'untranslated'),
		       count(*) FILTER (WHERE status = 'rejected')
		FROM segments WHERE file_id = $1`, fileID,
	).Scan(&st.Total, &st.Approved, &st.NeedsReview, &st.Draft, &st.Untranslated, &st.Rejected)
	if err != nil {
		return st, err
	}
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE q.severity = 'error'),
		       count(*) FILTER (WHERE q.severity = 'warning')
		FROM qa_issues q JOIN segments s ON s.id = q.segment_id
		WHERE s.file_id = $1`, fileID,
	).Scan(&st.Errors, &st.Warnings)
	return st, err
}
