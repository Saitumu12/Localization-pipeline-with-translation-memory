// Package api exposes the pipeline over HTTP for the React front end.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

type Server struct {
	pipe *pipeline.Pipeline
}

func New(p *pipeline.Pipeline) *Server { return &Server{pipe: p} }

// maxUpload caps an uploaded localization file. The largest fixture in this
// repository is about 2 MB.
const maxUpload = 32 << 20

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)

	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	mux.HandleFunc("GET /api/projects/{id}/files", s.listFiles)
	mux.HandleFunc("POST /api/projects/{id}/files", s.uploadFile)
	mux.HandleFunc("GET /api/projects/{id}/glossary", s.listGlossary)
	mux.HandleFunc("POST /api/projects/{id}/glossary", s.addGlossaryTerm)
	mux.HandleFunc("DELETE /api/projects/{id}/glossary/{termID}", s.deleteGlossaryTerm)
	mux.HandleFunc("GET /api/projects/{id}/inconsistencies", s.inconsistencies)

	mux.HandleFunc("GET /api/files/{id}/segments", s.listSegments)
	mux.HandleFunc("GET /api/files/{id}/stats", s.fileStats)
	mux.HandleFunc("GET /api/files/{id}/export", s.exportFile)
	mux.HandleFunc("POST /api/files/{id}/recheck", s.recheck)
	mux.HandleFunc("POST /api/files/{id}/adopt", s.adopt)

	mux.HandleFunc("PUT /api/segments/{id}", s.saveSegment)
	mux.HandleFunc("GET /api/segments/{id}/suggestions", s.suggestions)
	mux.HandleFunc("POST /api/segments/pretranslate", s.pretranslate)

	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"embeddings":          s.pipe.Embed != nil,
		"embedding_model":     s.pipe.Embed.Model(),
		"machine_translation": s.pipe.LLM != nil,
		"llm_model":           s.pipe.LLM.Model(),
	})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.pipe.Store.ListProjects(r.Context())
	respond(w, ps, err)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var p store.Project
	if !decode(w, r, &p) {
		return
	}
	if p.Name == "" || p.SourceLocale == "" || p.TargetLocale == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("name, source_locale and target_locale are required"))
		return
	}
	created, err := s.pipe.Store.CreateProject(r.Context(), p)
	respond(w, created, err)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	p, err := s.pipe.Store.GetProject(r.Context(), id)
	respond(w, p, err)
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	files, err := s.pipe.Store.ListFiles(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type withStats struct {
		store.File
		Stats store.FileStats `json:"stats"`
	}
	out := make([]withStats, 0, len(files))
	for _, f := range files {
		st, err := s.pipe.Store.StatsFor(r.Context(), f.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, withStats{File: f, Stats: st})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("a file field is required: %w", err))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUpload))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.pipe.Import(r.Context(), id, header.Filename, data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listGlossary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	terms, err := s.pipe.Store.Glossary(r.Context(), id)
	respond(w, terms, err)
}

func (s *Server) addGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var t store.GlossaryTerm
	if !decode(w, r, &t) {
		return
	}
	t.ProjectID = id
	if strings.TrimSpace(t.SourceTerm) == "" || strings.TrimSpace(t.TargetTerm) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("source_term and target_term are required"))
		return
	}
	saved, err := s.pipe.Store.AddGlossaryTerm(r.Context(), t)
	respond(w, saved, err)
}

func (s *Server) deleteGlossaryTerm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	termID, ok := pathID(w, r, "termID")
	if !ok {
		return
	}
	if err := s.pipe.Store.DeleteGlossaryTerm(r.Context(), id, termID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) inconsistencies(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	out, err := s.pipe.Store.InconsistentSources(r.Context(), id, intParam(r, "limit", 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// This is a read-only report, so show the text as a translator reads it
	// rather than the escaped XML that is stored.
	for i := range out {
		out[i].Source = format.PlainText(out[i].Source)
		for j, v := range out[i].Variants {
			out[i].Variants[j] = format.PlainText(v)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listSegments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	filter := store.SegmentFilter{
		Status:    r.URL.Query().Get("status"),
		Search:    r.URL.Query().Get("search"),
		OnlyIssue: r.URL.Query().Get("issues") == "1",
		Limit:     intParam(r, "limit", 50),
		Offset:    intParam(r, "offset", 0),
	}
	segs, total, err := s.pipe.Store.ListSegments(r.Context(), id, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"segments": segs, "total": total})
}

func (s *Server) fileStats(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	st, err := s.pipe.Store.StatsFor(r.Context(), id)
	respond(w, st, err)
}

func (s *Server) exportFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	onlyApproved := r.URL.Query().Get("approved_only") == "1"
	data, name, err := s.pipe.Export(r.Context(), id, onlyApproved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_, _ = w.Write(data)
}

func (s *Server) recheck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	n, err := s.pipe.Recheck(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"issues": n})
}

func (s *Server) adopt(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Reviewer string `json:"reviewer"`
	}
	if !decode(w, r, &body) {
		return
	}
	res, err := s.pipe.AdoptImported(r.Context(), id, strings.TrimSpace(body.Reviewer))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) saveSegment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req pipeline.SaveRequest
	if !decode(w, r, &req) {
		return
	}
	seg, err := s.pipe.Save(r.Context(), id, req)
	if err != nil {
		// A refused approval is the caller's problem to fix, not a server fault.
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, seg)
}

func (s *Server) suggestions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	matches, err := s.pipe.Suggest(r.Context(), id, intParam(r, "limit", 5), 0)
	respond(w, matches, err)
}

func (s *Server) pretranslate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SegmentIDs []int64 `json:"segment_ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	res, err := s.pipe.Pretranslate(r.Context(), body.SegmentIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// helpers

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s must be a number", name))
		return 0, false
	}
	return id, true
}

func intParam(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return false
	}
	return true
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, fmt.Errorf("not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already out, so all we can do is stop writing.
		return
	}
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
