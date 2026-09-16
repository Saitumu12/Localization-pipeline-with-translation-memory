// Command server runs the HTTP API and serves the review UI.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Saitumu12/localization-pipeline/internal/api"
	"github.com/Saitumu12/localization-pipeline/internal/config"
	"github.com/Saitumu12/localization-pipeline/internal/embed"
	"github.com/Saitumu12/localization-pipeline/internal/llm"
	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

func main() {
	cfg := config.FromEnv()
	ctx := context.Background()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatal(err)
	}

	em := embed.New(cfg.EmbeddingsURL)
	if err := em.Probe(ctx, config.EmbeddingDimensions); err != nil {
		log.Fatalf("%v\n(the translation memory needs the embedding service; try docker compose up -d)", err)
	}
	log.Printf("embeddings: %s (%d dimensions)", em.Model(), em.Dims())

	mt := llm.New(cfg.AnthropicKey, cfg.LLMModel).WithEndpoint(cfg.AnthropicBaseURL)
	if mt == nil {
		log.Print("machine translation: off (ANTHROPIC_API_KEY is not set)")
	} else {
		log.Printf("machine translation: %s", mt.Model())
	}

	p := pipeline.New(st, em, mt)
	handler := withUI(api.New(p).Routes(), cfg.WebDir)

	// Bind first so a port clash is reported instead of being printed after a
	// line claiming the server is up.
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("cannot listen on %s: %v", cfg.Addr, err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("listening on http://localhost%s", cfg.Addr)
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// withUI serves the built React app next to the API, falling back to index.html
// so client-side routes survive a page reload.
func withUI(apiHandler http.Handler, dir string) http.Handler {
	index := filepath.Join(dir, "index.html")
	if _, err := os.Stat(index); err != nil {
		log.Printf("UI not built (%s missing); serving the API only", index)
		return apiHandler
	}
	files := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if path := filepath.Join(dir, filepath.Clean(r.URL.Path)); r.URL.Path != "/" {
			if st, err := os.Stat(path); err == nil && !st.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, index)
	})
}
