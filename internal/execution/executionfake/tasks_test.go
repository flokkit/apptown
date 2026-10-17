package executionfake

import (
	"fmt"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executiontest"
)

func fakeSpec(key, output string, exit int, block bool) execution.TaskSpec {
	env := []string{"FAKE_OUTPUT=" + output, fmt.Sprintf("FAKE_EXIT=%d", exit)}
	if block {
		env = append(env, "FAKE_BLOCK=1")
	}
	return execution.TaskSpec{Key: key, Image: "fake", Argv: []string{"run"}, Env: env,
		Admission: execution.Admission{Profile: "fake", Network: execution.NetworkNone}}
}

// The fake keeps the contract its callers' tests rely on.
func TestTheFakeTaskDriverKeepsTheContract(t *testing.T) {
	executiontest.Run(t, executiontest.Harness{Driver: &Tasks{}, Spec: fakeSpec, Key: executiontest.UniqueKeys("fake")})
}
