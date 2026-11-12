package notification

import (
	"encoding/json"
	q "github.com/flokkit/apptown/internal/apptown/notification/internal/notificationsql"
	"strings"
	"time"
)

type Item struct {
	Key      string    `json:"key"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail"`
	Severity string    `json:"severity"`
	URL      string    `json:"url"`
	OpenedAt time.Time `json:"openedAt"`
	Section  string    `json:"section,omitempty"`
}
type Message struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Instance string   `json:"instance"`
	Item     *Item    `json:"item,omitempty"`
	Items    []Item   `json:"items,omitempty"`
	Title    string   `json:"title"`
	Lines    []string `json:"lines"`
	Link     string   `json:"link,omitempty"`
	Urgent   bool     `json:"urgent"`
}

func messageFor(kind string, rows []q.NotificationItem, origin string) Message {
	m := Message{Instance: "AppTown", Title: "AppTown daily digest", Lines: []string{}}
	for _, r := range rows {
		section := "Needs attention"
		switch {
		case r.ResolvedAt.Valid || kind == "resolved":
			section = "Cleared"
		case r.Unknown || r.Stale:
			section = "Not known"
		case r.Urgent:
			section = "Still open"
		}
		link := strings.TrimRight(origin, "/") + r.Href
		i := Item{r.Key, r.Title, r.Detail, r.Severity, link, r.OpenedAt, section}
		m.Items = append(m.Items, i)
		m.Lines = append(m.Lines, section+": "+r.Title, r.Detail, link)
	}
	if kind != "digest" && len(m.Items) > 0 {
		m.Item = &m.Items[0]
		m.Items = nil
		m.Title = m.Item.Title
		if kind == "resolved" {
			m.Title = "Cleared: " + m.Title
		}
		m.Link = m.Item.URL
		m.Urgent = kind == "opened" && rows[0].Urgent
	}
	return m
}
func render(m Message, format string) (string, []byte) {
	text := m.Title + "\n" + strings.Join(m.Lines, "\n")
	var body any
	switch format {
	case "ntfy":
		return "text/plain; charset=utf-8", []byte(text)
	case "slack":
		body = map[string]any{"text": text}
	case "discord":
		r := []rune(text)
		if len(r) > 2000 {
			r = r[:2000]
		}
		body = map[string]any{"content": string(r)}
	case "teams":
		body = map[string]any{"type": "message", "attachments": []any{map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "content": map[string]any{"type": "AdaptiveCard", "version": "1.4", "body": []any{map[string]any{"type": "TextBlock", "text": text, "wrap": true}}}}}}
	default:
		body = struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Instance string `json:"instance"`
			Item     *Item  `json:"item,omitempty"`
			Items    []Item `json:"items,omitempty"`
		}{m.ID, m.Kind, m.Instance, m.Item, m.Items}
	}
	b, _ := json.Marshal(body)
	return "application/json", b
}
