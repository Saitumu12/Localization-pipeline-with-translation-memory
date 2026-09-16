package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/api"
	"github.com/Saitumu12/localization-pipeline/internal/embed"
	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

// The handlers are thin, so what is worth testing here is the contract the UI
// relies on: a refused approval comes back as a 4xx with the reason, and an
// export comes back as a downloadable file.
func newServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	embedURL := os.Getenv("EMBEDDINGS_URL")
	if dsn == "" || embedURL == "" {
		t.Skip("TEST_DATABASE_URL and EMBEDDINGS_URL are not set; see README")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	em := embed.New(embedURL)
	if err := em.Probe(ctx, 384); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(pipeline.New(st, em, nil)).Routes())
	t.Cleanup(srv.Close)
	return srv, st
}

func do(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func uploadFixture(t *testing.T, base string, projectID int64, name string) int64 {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "oss", name))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(src); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	resp, err := http.Post(fmt.Sprintf("%s/api/projects/%d/files", base, projectID), mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: %s: %s", resp.Status, body)
	}
	var out struct {
		File store.File `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.File.ID
}

func TestUploadReviewAndExport(t *testing.T) {
	srv, st := newServer(t)
	ctx := context.Background()

	// A uniquely named project keeps this test independent of the others, which
	// run in a different package against the same database.
	name := fmt.Sprintf("%s-%d", t.Name(), rand.Int64())
	resp, body := do(t, "POST", srv.URL+"/api/projects", map[string]string{
		"name": name, "source_locale": "en", "target_locale": "de",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project: %s: %s", resp.Status, body)
	}
	var project store.Project
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM projects WHERE id = $1`, project.ID)
	})

	fixtureName := "symfony-validators.de.xlf"
	fileID := uploadFixture(t, srv.URL, project.ID, fixtureName)

	// The import is visible through the listing, with progress figures.
	resp, body = do(t, "GET", fmt.Sprintf("%s/api/projects/%d/files", srv.URL, project.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list files: %s: %s", resp.Status, body)
	}
	if !strings.Contains(string(body), `"total":117`) {
		t.Errorf("expected 117 segments in the listing: %s", body)
	}

	segs, err := st.FileSegments(ctx, fileID)
	if err != nil {
		t.Fatal(err)
	}
	var withPlaceholder store.Segment
	for _, s := range segs {
		if strings.Contains(s.SourceText, "{{ limit }}") {
			withPlaceholder = s
			break
		}
	}
	if withPlaceholder.ID == 0 {
		t.Fatal("no segment with a placeholder in the fixture")
	}

	// Approving without a reviewer is the caller's mistake, so it must come back
	// as a 400 carrying the reason rather than a 500.
	resp, body = do(t, "PUT", fmt.Sprintf("%s/api/segments/%d", srv.URL, withPlaceholder.ID),
		map[string]any{"target": withPlaceholder.TargetText, "approve": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %s, want 400: %s", resp.Status, body)
	}
	if !strings.Contains(string(body), "reviewer") {
		t.Errorf("body should explain what is missing: %s", body)
	}

	// Dropping a placeholder is refused with the finding in the message.
	resp, body = do(t, "PUT", fmt.Sprintf("%s/api/segments/%d", srv.URL, withPlaceholder.ID),
		map[string]any{"target": "Ohne Platzhalter", "approve": true, "reviewer": "sai"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %s, want 400: %s", resp.Status, body)
	}
	if !strings.Contains(string(body), "placeholder mismatch") {
		t.Errorf("body should name the finding: %s", body)
	}

	// The real translation approves, and the response carries the reviewer.
	resp, body = do(t, "PUT", fmt.Sprintf("%s/api/segments/%d", srv.URL, withPlaceholder.ID),
		map[string]any{"target": withPlaceholder.TargetText, "approve": true, "reviewer": "sai"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %s: %s", resp.Status, body)
	}
	var saved store.Segment
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "approved" || saved.ReviewedBy == nil || *saved.ReviewedBy != "sai" {
		t.Errorf("segment = %+v", saved)
	}

	// That approval is immediately available as an exact memory suggestion.
	resp, body = do(t, "GET", fmt.Sprintf("%s/api/segments/%d/suggestions", srv.URL, withPlaceholder.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suggestions: %s: %s", resp.Status, body)
	}

	// Export is served as a download under the original file name.
	resp, body = do(t, "GET", fmt.Sprintf("%s/api/files/%d/export", srv.URL, fileID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %s", resp.Status)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, fixtureName) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !bytes.HasPrefix(body, []byte("<?xml")) {
		t.Errorf("export does not look like XML: %.60s", body)
	}
}

func TestUnknownIDsAreNotFound(t *testing.T) {
	srv, _ := newServer(t)

	resp, _ := do(t, "GET", srv.URL+"/api/projects/999999", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %s, want 404", resp.Status)
	}
	resp, _ = do(t, "GET", srv.URL+"/api/projects/not-a-number", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %s, want 400", resp.Status)
	}
}

func TestPretranslateWithoutAModelIsRefused(t *testing.T) {
	srv, _ := newServer(t)
	resp, body := do(t, "POST", srv.URL+"/api/segments/pretranslate",
		map[string]any{"segment_ids": []int64{1}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %s, want 400", resp.Status)
	}
	if !strings.Contains(string(body), "ANTHROPIC_API_KEY") {
		t.Errorf("body should say what is missing: %s", body)
	}
}
