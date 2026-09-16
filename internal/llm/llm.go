// Package llm asks a model for a first-pass translation.
//
// Everything here returns text and nothing else. It cannot set a status, mark a
// segment reviewed or write to the translation memory, so a suggestion can only
// ever enter the pipeline as something a person still has to look at.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Saitumu12/localization-pipeline/internal/checks"
)

const (
	defaultModel  = "claude-sonnet-5"
	defaultAPIURL = "https://api.anthropic.com/v1/messages"
	apiVersion    = "2023-06-01"
	batchLimit    = 20
)

type Client struct {
	apiKey string
	model  string
	url    string
	http   *http.Client
}

// New returns a client, or nil when no API key is configured. Callers must
// treat a nil client as "machine translation is switched off" rather than
// inventing text.
func New(apiKey, model string) *Client {
	if apiKey == "" {
		return nil
	}
	if model == "" {
		model = defaultModel
	}
	return &Client{
		apiKey: apiKey,
		model:  model,
		url:    defaultAPIURL,
		http:   &http.Client{Timeout: 120 * time.Second},
	}
}

// WithEndpoint points the client at a different Messages API endpoint. Tests
// use it to serve a stub; ANTHROPIC_BASE_URL sets it in production for anyone
// running behind a gateway.
func (c *Client) WithEndpoint(url string) *Client {
	if c != nil && url != "" {
		c.url = strings.TrimRight(url, "/") + "/v1/messages"
	}
	return c
}

func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// Request is one string to translate.
type Request struct {
	Source   string // raw inner XML of the source segment
	Note     string
	MaxWidth int
}

type promptItem struct {
	Index    int    `json:"index"`
	Source   string `json:"source"`
	Note     string `json:"note,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
}

// Translate returns one draft per request, in order. A draft is just a string;
// it is the caller's job to store it as unreviewed.
func (c *Client) Translate(ctx context.Context, sourceLocale, targetLocale string, glossary []checks.Term, reqs []Request) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("llm: machine translation is not configured (set ANTHROPIC_API_KEY)")
	}
	if len(reqs) == 0 {
		return nil, nil
	}
	if len(reqs) > batchLimit {
		return nil, fmt.Errorf("llm: at most %d strings per request, got %d", batchLimit, len(reqs))
	}

	items := make([]promptItem, len(reqs))
	for i, r := range reqs {
		items[i] = promptItem{Index: i, Source: r.Source, Note: r.Note, MaxChars: r.MaxWidth}
	}
	payload, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}

	text, err := c.complete(ctx, systemPrompt(sourceLocale, targetLocale, glossary), string(payload))
	if err != nil {
		return nil, err
	}

	var out []string
	if err := json.Unmarshal([]byte(stripFence(text)), &out); err != nil {
		return nil, fmt.Errorf("llm: model did not return a JSON array: %w (got %.200q)", err, text)
	}
	if len(out) != len(reqs) {
		return nil, fmt.Errorf("llm: asked for %d translations, got %d", len(reqs), len(out))
	}
	return out, nil
}

func systemPrompt(sourceLocale, targetLocale string, glossary []checks.Term) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You translate software interface strings from %s to %s.\n\n", sourceLocale, targetLocale)
	b.WriteString(`Each source string is an XML fragment taken from a localization file.

Rules:
- Reproduce every placeholder exactly: %1, %L2, %n, %s, %d, %@, %1$@, {name}, {{ name }},
  and inline elements such as <x id="INTERPOLATION"/>. Do not add, drop, renumber or
  translate them. You may move them to fit the grammar of the target language.
- Keep any inline XML elements intact and in the output.
- The output is XML text, so write & as &amp;, < as &lt; and > as &gt;.
- Translate the user-visible wording only. Do not explain, annotate or add quotes.
- If max_chars is given, stay within it.
`)
	if len(glossary) > 0 {
		b.WriteString("\nUse this terminology:\n")
		for _, t := range glossary {
			fmt.Fprintf(&b, "- %q -> %q\n", t.Source, t.Target)
		}
	}
	b.WriteString("\nReturn a JSON array of strings: the translations in the same order as the input, and nothing else.")
	return b.String()
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type apiRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type apiResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) complete(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(apiRequest{
		Model:     c.model,
		MaxTokens: 4096,
		System:    system,
		Messages:  []message{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", apiVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var parsed apiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("llm: %s: %.300s", resp.Status, raw)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("llm: %s: %s", parsed.Error.Type, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: %s: %.300s", resp.Status, raw)
	}

	var b strings.Builder
	for _, part := range parsed.Content {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("llm: empty response")
	}
	return b.String(), nil
}

// stripFence tolerates a model that wraps its JSON in a markdown code fence.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// BatchLimit is how many strings one Translate call accepts.
const BatchLimit = batchLimit
