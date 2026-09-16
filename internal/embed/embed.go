// Package embed turns text into vectors by calling the embedding service that
// runs alongside the database. The Go process does not host a model itself.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client

	model string
	dims  int
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

type health struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

// Probe asks the service which model it is serving. The dimension has to match
// the vector column width, so a mismatch is worth failing on at startup rather
// than on the first insert.
func (c *Client) Probe(ctx context.Context, wantDims int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("embed: %s is not reachable: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	var h health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return fmt.Errorf("embed: bad health response: %w", err)
	}
	if h.Dimensions != wantDims {
		return fmt.Errorf("embed: service returns %d dimensions but the database column is vector(%d)", h.Dimensions, wantDims)
	}
	c.model, c.dims = h.Model, h.Dimensions
	return nil
}

// Model is safe on a nil client so callers can report "not configured".
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

func (c *Client) Dims() int {
	if c == nil {
		return 0
	}
	return c.dims
}

type embedRequest struct {
	Texts []string `json:"texts"`
}

type embedResponse struct {
	Model   string      `json:"model"`
	Vectors [][]float32 `json:"vectors"`
}

// Embed returns one vector per input text, in the same order.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Texts: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embed: service returned %s: %s", resp.Status, msg)
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: bad response: %w", err)
	}
	if len(out.Vectors) != len(texts) {
		return nil, fmt.Errorf("embed: asked for %d vectors, got %d", len(texts), len(out.Vectors))
	}
	return out.Vectors, nil
}

// EmbedOne is the common single-string case.
func (c *Client) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	vs, err := c.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}
