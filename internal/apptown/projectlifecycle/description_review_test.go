package projectlifecycle

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
)

func TestReviewDescriptionPreservesCSharp(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{Slug: "csharp-tools", Actor: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	const reply = "Tools for C#"
	d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) { return reply, nil, nil })}
	if err := d.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	held, err := f.lifecycle.projects.ByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Blurb != reply {
		t.Fatalf("published %q; want %q", held.Blurb, reply)
	}
}

func TestDescriptionPreservesLiteralPunctuation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ reply, want string }{
		{"C# tools", "C# tools"},
		{"#1 fan site", "#1 fan site"},
		{"### Tools for C#", "Tools for C#"},
		{"### Tools for C# ###", "Tools for C#"},
		{"**Tools for C#**", "Tools for C#"},
		{"*star tools", "*star tools"},
		{"Tools for star*", "Tools for star*"},
		{"_private tools", "_private tools"},
		{"Tools for private_", "Tools for private_"},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			if got := normaliseDescription(tc.reply); got != tc.want {
				t.Fatalf("normalised %q; want %q", got, tc.want)
			}
		})
	}
}
