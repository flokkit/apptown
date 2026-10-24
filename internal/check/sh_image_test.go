package check_test

import (
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/runtimeimages"
)

// An sh check runs in the shell image of the tool versions the process
// loaded, whatever it is: never a name compiled into the evaluator.
// Not parallel: it sets the process's images, and restores them.
func TestAnShCheckRunsInTheLoadedShellImage(t *testing.T) {
	prev := runtimeimages.Images{Sh: runtimeimages.Sh(), DF: runtimeimages.DF(), StaticServer: runtimeimages.StaticServer(), PrivateDocker: runtimeimages.PrivateDocker()}
	t.Cleanup(func() { runtimeimages.Set(prev) })
	loaded := prev
	loaded.Sh = "example.invalid/sh@sha256:" + "0000000000000000000000000000000000000000000000000000000000000001"
	runtimeimages.Set(loaded)
	var image string
	j := &check.Judge{Run: func(req execution.Request) execution.Outcome {
		image = req.Image
		return execution.Outcome{}
	}}
	j.EvaluateSh([]check.Declared{sourced("b", 1, "c", "true", "high")}, check.Source{Tree: t.TempDir()}, check.Gate, "img")
	if image != loaded.Sh {
		t.Fatalf("the check ran in %q, not the loaded %q", image, loaded.Sh)
	}
}
