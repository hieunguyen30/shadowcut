package explain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/oauth2/google"
)

// Vertex calls Gemini on Vertex AI (generateContent). It requires Application
// Default Credentials and a project with the Vertex AI API enabled. Not exercised
// locally: no cloud credentials were used for this project (evaluation pending).
type Vertex struct {
	Project  string
	Location string
	Model    string // e.g. "gemini-2.5-flash"; set to the current model id
	HTTP     *http.Client
}

// Name implements Explainer.
func (v *Vertex) Name() string { return "vertex:" + v.Model }

const prompt = `You are helping migrate an application from PostgreSQL to Cloud Spanner's PostgreSQL interface.
A shadow read returned different results on the two databases.

Query:
%s

Root-cause category (from Shadowcut's classifier): %s
Classifier evidence: %s

Reply with JSON only: {"summary": "<2 sentences for an application developer>",
"fix": "<what to change>", "fixed_sql": "<a rewritten query that returns the same result on both databases and keeps the PostgreSQL semantics, or empty if no SQL-only fix exists>"}`

// Explain implements Explainer.
func (v *Vertex) Explain(ctx context.Context, in Input) (Explanation, error) {
	if v.HTTP == nil {
		c, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return Explanation{}, fmt.Errorf("vertex: credentials: %w", err)
		}
		v.HTTP = c
	}
	body, _ := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": fmt.Sprintf(prompt, in.SQL, in.Category, in.Reason)}}}},
		"generationConfig": map[string]any{"temperature": 0, "responseMimeType": "application/json"},
	})
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent", v.Location, v.Project, v.Location, v.Model)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return Explanation{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Explanation{}, fmt.Errorf("vertex: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return Explanation{}, fmt.Errorf("vertex: unexpected response: %.200s", raw)
	}
	var e Explanation
	if err := json.Unmarshal([]byte(out.Candidates[0].Content.Parts[0].Text), &e); err != nil {
		return Explanation{}, fmt.Errorf("vertex: model did not return JSON: %w", err)
	}
	e.Model = v.Name()
	return e, nil
}
