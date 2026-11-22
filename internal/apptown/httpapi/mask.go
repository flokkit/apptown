package httpapi

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/strs"
)

// mask gives a function replacing known secret values in text AppTown
// shows, from one read of the stores (secret.Service.Masker); nil shows
// text as it is.
type mask func(context.Context) func(string) string

// messages is a chat's messages as a view shows them: their words and tool
// details masked. The messages given are not changed.
func (m mask) messages(ctx context.Context, msgs []console.Message) []console.Message {
	return maskMessages(m.snapshot(ctx), msgs) // one snapshot for every message
}

// snapshot is one read of the values to mask; nil when nothing is masked.
func (m mask) snapshot(ctx context.Context) func(string) string {
	if m == nil {
		return nil
	}
	return m(ctx)
}

// maskDraft is the message the helper is still writing, masked as text
// still being written (secret.Mask.Partial): its unfinished last word is
// withheld. A draft whose masking cannot be decided, or with nothing left
// to show, is not sent.
func maskDraft(mask secret.Mask, draft *console.Message) *console.Message {
	if draft == nil {
		return nil
	}
	out := *draft
	text, ok := mask.Partial(out.Text)
	if !ok {
		return nil
	}
	out.Text = text
	if text == "" && len(out.Tools) == 0 {
		return nil
	}
	if len(out.Tools) > 0 {
		out.Tools = append(out.Tools[:0:0], out.Tools...)
		for j := range out.Tools {
			detail, decided := mask.Partial(out.Tools[j].Detail) // uncut: see console.ToolDetailMax
			if !decided {
				return nil
			}
			out.Tools[j].Detail = strs.Truncate(detail, console.ToolDetailMax)
		}
	}
	return &out
}

// maskMessages is messages masked with one snapshot (nil masks nothing).
func maskMessages(mask func(string) string, msgs []console.Message) []console.Message {
	if mask == nil {
		return msgs
	}
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
