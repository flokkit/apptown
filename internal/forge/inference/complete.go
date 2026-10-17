package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Complete makes one tool-free, non-streaming model call for a trusted
// caller inside the forge (drafting a workflow). It goes through the same
// admission as a runtime's request (ServeHTTP): the token's current
// workload and account, the provider binding, the model's eligibility, the
// receipt written before the upstream call, cancellation through ctx, and
// usage recorded once (unknown when the provider reports none). Only the
// network check is skipped: the caller is the forge itself, not a peer.
//
// model is "<binding>/<model id>", as a model slot names it.
func (g *Gateway) Complete(ctx context.Context, token, model, system, prompt string) (string, error) {
	name, id, ok := strings.Cut(model, "/")
	if !ok || id == "" || strings.ContainsAny(model, "*~") {
		return "", fmt.Errorf("the model %q does not name one model of a configured provider", model)
	}
	ps, err := g.providers(ctx)
	if err != nil {
		return "", fmt.Errorf("model provider configuration is unavailable: %w", err)
	}
	p := ps[name]
	if p == nil {
		return "", fmt.Errorf("unknown inference provider %q", name)
	}
	route, body := completionRequest(p.api, id, system, prompt)
	raw, _ := json.Marshal(body)
	r, err := http.NewRequestWithContext(ctx, "POST", "/providers/"+name+"/"+route, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/json")
	if p.api == "anthropic-messages" {
		r.Header.Set("Anthropic-Version", "2023-06-01")
	}
	out := &captured{header: http.Header{}}
	g.admitted(out, r)
	if out.status != http.StatusOK {
		return "", fmt.Errorf("%s", strings.TrimSpace(out.body.String()))
	}
	return completionText(p.api, out.body.Bytes())
}

// captured is the response of an in-process call.
type captured struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *captured) Header() http.Header { return c.header }
func (c *captured) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *captured) Write(b []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	return c.body.Write(b)
}

// completionRequest is one system and one user message in the provider's
// own dialect, with no tools.
func completionRequest(api, model, system, prompt string) (string, map[string]any) {
	switch api {
	case "anthropic-messages":
		return "v1/messages", map[string]any{"model": model, "max_tokens": 8192, "system": system,
			"messages": []map[string]string{{"role": "user", "content": prompt}}}
	case "openai-responses":
		return "responses", map[string]any{"model": model, "instructions": system, "input": prompt}
	case "google-generative-ai":
		text := func(s string) []map[string]string { return []map[string]string{{"text": s}} }
		return "models/" + model + ":generateContent", map[string]any{"systemInstruction": map[string]any{"parts": text(system)},
			"contents": []map[string]any{{"role": "user", "parts": text(prompt)}}}
	}
	return "chat/completions", map[string]any{"model": model, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": prompt}}}
}

// completionText reads the model's text from a non-streaming response.
func completionText(api string, body []byte) (string, error) {
	var text strings.Builder
	var err error
	switch api {
	case "anthropic-messages":
		var r struct {
			Content []struct{ Type, Text string }
		}
		err = json.Unmarshal(body, &r)
		for _, c := range r.Content {
			if c.Type == "text" {
				text.WriteString(c.Text)
			}
		}
	case "openai-responses":
		var r struct {
			Output []struct {
				Content []struct{ Type, Text string }
			}
		}
		err = json.Unmarshal(body, &r)
		for _, o := range r.Output {
			for _, c := range o.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
		}
	case "google-generative-ai":
		var r struct {
			Candidates []struct {
				Content struct{ Parts []struct{ Text string } }
			}
		}
		err = json.Unmarshal(body, &r)
		if len(r.Candidates) > 0 {
			for _, part := range r.Candidates[0].Content.Parts {
				text.WriteString(part.Text)
			}
		}
	default:
		var r struct {
			Choices []struct{ Message struct{ Content string } }
		}
		err = json.Unmarshal(body, &r)
		if len(r.Choices) > 0 {
			text.WriteString(r.Choices[0].Message.Content)
		}
	}
	if err != nil || text.Len() == 0 {
		return "", fmt.Errorf("the model's response had no text")
	}
	return text.String(), nil
}
