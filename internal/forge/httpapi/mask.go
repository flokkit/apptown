package httpapi

import (
	"context"

	"flokkit.io/forge/internal/console"
)

// Mask gives a function replacing known secret values in text AppTown
// shows, from one read of the stores (secret.Service.Masker); nil shows
// text as it is.
type Mask func(context.Context) func(string) string

// Messages is a chat's messages as a view shows them: their words and tool
// details masked. The messages given are not changed.
func (m Mask) Messages(ctx context.Context, msgs []console.Message) []console.Message {
	if m == nil {
		return msgs
	}
	mask := m(ctx) // one snapshot for every message
	out := make([]console.Message, len(msgs))
	for i, msg := range msgs {
		msg.Text = mask(msg.Text)
		if len(msg.Tools) > 0 {
			tools := append(msg.Tools[:0:0], msg.Tools...)
			for j := range tools {
				tools[j].Detail = mask(tools[j].Detail)
			}
			msg.Tools = tools
		}
		out[i] = msg
	}
	return out
}
