package execution_test

import (
	"github.com/flokkit/apptown/internal/execution"
	"testing"
	"time"
)

func TestGeneratedFilesCannotReplaceCommittedCheckSource(t *testing.T) {
	file := execution.File{Path: "/src/mise.toml", Content: []byte("fake evidence"), Mode: 0o644}
	if err := execution.ValidFiles([]execution.File{file}); err != nil {
		t.Fatalf("ordinary generated-file contract changed: %v", err)
	}
	for _, remote := range []bool{false, true} {
		spec := execution.TaskSpec{Key: "check", Image: "agent", Argv: []string{"pi"}, Admission: execution.Admission{Profile: "fixture", Network: execution.NetworkNone}, Files: []execution.File{file}}
		if remote {
			spec.SourceAtSrc = true
			spec.Source = &execution.TaskSource{Repository: "hub", Commit: "commit"}
		} else {
			spec.ReadOnlySource = "tree"
		}
		if err := spec.Validate(time.Now()); err == nil {
			t.Fatalf("remote=%t: generated file replaced committed source", remote)
		}
	}
}
