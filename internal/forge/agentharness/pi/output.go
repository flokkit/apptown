package pi

import (
	"encoding/json"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/strs"
)

// ParseOutput extracts all assistant text plus model metadata from a pi
// --mode json event stream.
func ParseOutput(out []byte) (text, model, provider string, usage map[string]any) {
	var allTexts []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Model    string         `json:"model"`
				Provider string         `json:"provider"`
				Usage    map[string]any `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "message_end" && ev.Message.Role == "assistant" {
			for _, c := range ev.Message.Content {
				if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
					allTexts = append(allTexts, c.Text)
				}
			}
			model, provider, usage = ev.Message.Model, ev.Message.Provider, ev.Message.Usage
		}
	}
	return strings.Join(allTexts, "\n\n"), model, provider, usage
}

// SummarizeEvent turns one pi JSON event line into zero or more compact
// display lines. Unknown/chatty events are dropped; non-JSON lines pass
// through (subprocess stderr, docker noise).
func SummarizeEvent(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return []string{trimmed}
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"content"`
			Usage struct {
				Output      float64 `json:"output"`
				TotalTokens float64 `json:"totalTokens"`
			} `json:"usage"`
		} `json:"message"`
		ToolResults []json.RawMessage `json:"toolResults"`
	}
	if json.Unmarshal([]byte(trimmed), &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "message_end":
		if ev.Message.Role != "assistant" {
			return nil
		}
		var out []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				t := strings.TrimSpace(c.Text)
				if t != "" {
					t = strs.Truncate(t, 400)
					out = append(out, strings.Split(t, "\n")...)
				}
			case "thinking":
				// drop: noisy, and reasoning is in the session file
			default:
				// tool call variants — show name + arg preview
				arg := strings.TrimSpace(string(c.Arguments))
				arg = strs.Truncate(arg, 160)
				name := c.Name
				if name == "" {
					name = c.Type
				}
				out = append(out, fmt.Sprintf("→ %s %s", name, arg))
			}
		}
		return out
	case "turn_end":
		if n := len(ev.ToolResults); n > 0 {
			return []string{fmt.Sprintf("✔ %d tool result(s) · %.0f tok out", n, ev.Message.Usage.Output)}
		}
		return nil
	case "agent_end":
		return []string{"■ agent finished"}
	}
	return nil
}

// LastModelError: the provider's own words when a turn ended in an error
// (pi records stopReason "error" + errorMessage on the turn). Empty when
// the run ended normally. Unwraps the JSON-in-a-string body providers
// send so the reason reads as a sentence, not an escaped blob.
func LastModelError(out []byte) string {
	last := ""
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, `"stopReason":"error"`) {
			continue
		}
		var ev struct {
			Message struct {
				ErrorMessage string `json:"errorMessage"`
			} `json:"message"`
			ErrorMessage string `json:"errorMessage"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		msg := ev.Message.ErrorMessage
		if msg == "" {
			msg = ev.ErrorMessage
		}
		if msg == "" {
			continue
		}
		// "400 {"type":"error","error":{"message":"..."}}" → the message.
		if i := strings.Index(msg, "{"); i >= 0 {
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(msg[i:]), &body) == nil && body.Error.Message != "" {
				msg = strings.TrimSpace(msg[:i]) + " " + body.Error.Message
			}
		}
		last = strings.TrimSpace(msg)
	}
	return last
}
