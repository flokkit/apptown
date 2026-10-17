package preview

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
)

// A run's preview boots only Work's admitted execution of a preview step,
// and a cancelled caller is refused before any effect.
func TestARunPreviewRefusesUnclaimedWorkWithoutSideEffects(t *testing.T) {
	t.Parallel()
	s := New(nil, Dependencies{})
	for _, claim := range []deployment.ClaimedExecution{nil, (*workflow.Execution)(nil), &workflow.Execution{}} {
		if _, err := s.RunPreview(context.Background(), claim, RunInput{}); err == nil {
			t.Fatal("preview accepted unclaimed work")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.RunPreview(ctx, nil, RunInput{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
