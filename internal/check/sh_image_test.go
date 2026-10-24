package check_test

import (
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// An sh check runs in the shell image of the tool versions, by digest: never
// a name compiled into the evaluator.
func TestAnShCheckRunsInTheToolVersionsShellImage(t *testing.T) {
	t.Parallel()
	var image string
	j := &check.Judge{ShImage: testShImage, Run: func(req execution.Request) execution.Outcome {
		image = req.Image
		return execution.Outcome{}
	}}
	j.EvaluateSh([]check.Declared{sourced("b", 1, "c", "true", "high")}, check.Source{Tree: t.TempDir()}, check.Gate, "img")
	if want := stock.Images().Sh; image != want {
		t.Fatalf("the check ran in %q, not the tool versions' %q", image, want)
	}
}

// testShImage is the shell image test judges are given, as a caller's
// snapshot would give it.
var testShImage = stock.Images().Sh
