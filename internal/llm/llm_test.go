package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
	"github.com/Saitumu12/localization-pipeline/internal/llm"
)

// captured is what the stub server saw, so the request the client builds can be
// asserted without calling the real API.
type captured struct {
	apiKey  string
	version string
	body    struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    string `json:"system"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
}

func stub(t *testing.T, reply string, status int) (*llm.Client, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.apiKey = r.Header.Get("x-api-key")
		got.version = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return llm.New("secret", "claude-sonnet-5").WithEndpoint(srv.URL), got
}

func textReply(s string) string {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": s}},
	})
	return string(b)
}

func TestTranslateBuildsTheRequest(t *testing.T) {
	client, got := stub(t, textReply(`["Datei öffnen","%1 von %2"]`), http.StatusOK)

	out, err := client.Translate(context.Background(), "en", "de",
		[]checks.Term{{Source: "file", Target: "Datei"}},
		[]llm.Request{
			{Source: "Open file", Note: "menu item"},
			{Source: "%1 of %2", MaxWidth: 20},
		})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(out, "|") != "Datei öffnen|%1 von %2" {
		t.Errorf("translations = %q", out)
	}

	if got.apiKey != "secret" || got.version == "" {
		t.Errorf("auth headers = %q / %q", got.apiKey, got.version)
	}
	if got.body.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", got.body.Model)
	}
	if !strings.Contains(got.body.System, "from en to de") {
		t.Errorf("system prompt should name the locales:\n%s", got.body.System)
	}
	if !strings.Contains(got.body.System, `"file" -> "Datei"`) {
		t.Errorf("system prompt should carry the glossary:\n%s", got.body.System)
	}
	if !strings.Contains(got.body.System, "placeholder") && !strings.Contains(got.body.System, "%1") {
		t.Errorf("system prompt should demand the placeholders be kept:\n%s", got.body.System)
	}
	if len(got.body.Messages) != 1 || got.body.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", got.body.Messages)
	}
	// The sources and the per-string hints have to reach the model.
	for _, want := range []string{"Open file", "menu item", "%1 of %2", "20"} {
		if !strings.Contains(got.body.Messages[0].Content, want) {
			t.Errorf("user message is missing %q:\n%s", want, got.body.Messages[0].Content)
		}
	}
}

func TestTranslateAcceptsAFencedReply(t *testing.T) {
	client, _ := stub(t, textReply("```json\n[\"Hallo\"]\n```"), http.StatusOK)
	out, err := client.Translate(context.Background(), "en", "de", nil, []llm.Request{{Source: "Hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "Hallo" {
		t.Errorf("out = %q", out)
	}
}

func TestTranslateRejectsAShortReply(t *testing.T) {
	client, _ := stub(t, textReply(`["only one"]`), http.StatusOK)
	_, err := client.Translate(context.Background(), "en", "de", nil,
		[]llm.Request{{Source: "a"}, {Source: "b"}})
	if err == nil || !strings.Contains(err.Error(), "got 1") {
		t.Fatalf("expected a count mismatch, got %v", err)
	}
}

func TestTranslateRejectsNonJSON(t *testing.T) {
	client, _ := stub(t, textReply("Sure! Here are your translations."), http.StatusOK)
	_, err := client.Translate(context.Background(), "en", "de", nil, []llm.Request{{Source: "a"}})
	if err == nil || !strings.Contains(err.Error(), "JSON array") {
		t.Fatalf("expected a parse error, got %v", err)
	}
}

func TestTranslateSurfacesAPIErrors(t *testing.T) {
	body := `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	client, _ := stub(t, body, http.StatusUnauthorized)
	_, err := client.Translate(context.Background(), "en", "de", nil, []llm.Request{{Source: "a"}})
	if err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("expected the API message to be passed through, got %v", err)
	}
}

// Without an API key there is no client at all, so nothing can quietly invent a
// translation.
func TestNoKeyMeansNoClient(t *testing.T) {
	var client *llm.Client = llm.New("", "")
	if client != nil {
		t.Fatal("expected a nil client without an API key")
	}
	if _, err := client.Translate(context.Background(), "en", "de", nil, []llm.Request{{Source: "a"}}); err == nil {
		t.Fatal("expected an error from a nil client")
	}
	if client.Model() != "" {
		t.Error("a nil client should report no model")
	}
}

func TestBatchLimitIsEnforced(t *testing.T) {
	client, _ := stub(t, textReply("[]"), http.StatusOK)
	reqs := make([]llm.Request, llm.BatchLimit+1)
	if _, err := client.Translate(context.Background(), "en", "de", nil, reqs); err == nil {
		t.Fatal("expected an error above the batch limit")
	}
}
