package check_test

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// How long an sh check took is measured on the judge's clock.
func TestAnShChecksDurationIsTheJudgesClocks(t *testing.T) {
	t.Parallel()
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	j := &check.Judge{ShImage: testShImage,
		Now: func() time.Time { return at },
		Run: func(execution.Request) execution.Outcome {
			at = at.Add(7 * time.Second) // the run takes seven seconds on the judge's clock
			return execution.Outcome{}
		},
	}
	evs := j.EvaluateSh([]check.Declared{sourced("lang/hygiene", 1, "quick", "exit 0", "low")}, check.Source{Tree: t.TempDir()}, check.Gate, "t-clock")
	if len(evs) != 1 || evs[0].Evidence.Duration != 7*time.Second {
		t.Fatalf("an evaluation seven seconds long on the judge's clock: %+v", evs)
	}
}
