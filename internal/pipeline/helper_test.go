package pipeline_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/embed"
	"github.com/Saitumu12/localization-pipeline/internal/llm"
	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

// These tests talk to a real Postgres with pgvector and to the real embedding
// service, because that is the only way the pgvector query and the database
// constraints are actually exercised. Run "docker compose up -d" first.
func newPipeline(t *testing.T) (*pipeline.Pipeline, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; run docker compose up -d and see README")
	}
	embedURL := os.Getenv("EMBEDDINGS_URL")
	if embedURL == "" {
		t.Skip("EMBEDDINGS_URL is not set; run docker compose up -d and see README")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	em := embed.New(embedURL)
	if err := em.Probe(ctx, 384); err != nil {
		t.Fatalf("embedding service: %v", err)
	}
	return pipeline.New(st, em, nil), st
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "oss", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every test works inside its own project rather than truncating shared
// tables, so packages can run concurrently without fighting over the database.
func newProject(t *testing.T, st *store.Store, target string) store.Project {
	t.Helper()
	p, err := st.CreateProject(context.Background(), store.Project{
		Name:         uniqueName(t),
		SourceLocale: "en",
		TargetLocale: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, p.ID)
	})
	return p
}

// runID separates one test run from another, so a run that was interrupted
// before its cleanup ran cannot collide with the next one.
var (
	runID       = rand.Int64()
	nameCounter atomic.Int64
)

func uniqueName(t *testing.T) string {
	return fmt.Sprintf("%s-%d-%d", t.Name(), runID, nameCounter.Add(1))
}

// stubAnthropic serves the Messages API shape so the LLM code path can be
// tested without a network call. replies is the JSON array the model returns.
func stubAnthropic(t *testing.T, replies []string) *llm.Client {
	t.Helper()
	body, err := json.Marshal(replies)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" || r.Header.Get("anthropic-version") == "" {
			http.Error(w, "missing auth headers", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(body)}},
		})
	}))
	t.Cleanup(srv.Close)
	return llm.New("test-key", "claude-sonnet-5").WithEndpoint(srv.URL)
}
