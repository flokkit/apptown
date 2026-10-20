package docker

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"text/template"
	"time"
)

var errFake = errors.New("exit status 1")

func TestDockerInspectionDistinguishesAbsenceAndFailure(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"Error: No such object: selected\n", "Error response from daemon: No such container: selected\n", "error: no such object: selected\n"} {
		if _, err := decodeRuntime([]byte(line), errFake, "selected"); !errors.Is(err, ErrContainerAbsent) {
			t.Fatalf("missing container not recognized: %v", err)
		}
	}
	for _, line := range []string{"Cannot connect to the Docker daemon", "Error: No such object: selected-copy\n"} {
		if _, err := decodeRuntime([]byte(line), errFake, "selected"); err == nil || errors.Is(err, ErrContainerAbsent) {
			t.Fatalf("failed inspection treated as absence: %v", err)
		}
	}
	if _, err := decodeRuntime([]byte("not JSON"), nil, "selected"); err == nil || errors.Is(err, ErrContainerAbsent) {
		t.Fatalf("invalid runtime response: %v", err)
	}
}

// The inspect format AppTown sends is a template the daemon fills from
// its own record: rendered over a container shaped as docker inspect
// shapes one, it reads back the process state the daemon kept (an
// out-of-memory kill's exit, the restart counter, the times), and a
// container with no health check says none rather than healthy. An answer
// without a state is not reported, never a clean exit.
func TestTheRuntimeFormatReadsTheDaemonsProcessState(t *testing.T) {
	t.Parallel()
	// Docker renders the format with missingkey=error over the inspected
	// object, as here: a key a container lacks is an error.
	tmpl := template.Must(template.New("inspect").Option("missingkey=error").Funcs(template.FuncMap{"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	}}).Parse(RuntimeFormat))
	container := map[string]any{
		"Id": "c0ffee", "Image": "sha256:app", "RestartCount": 4,
		"Config":     map[string]any{"Labels": map[string]string{"forge.generation": "3"}},
		"HostConfig": map[string]any{"Memory": 268435456},
		"State": map[string]any{"Status": "exited", "Running": false, "OOMKilled": true, "ExitCode": 137,
			"StartedAt": "2026-10-16T01:02:03.5Z", "FinishedAt": "2026-10-16T01:04:05Z"},
	}
	render := func() Runtime {
		var out bytes.Buffer
		if err := tmpl.Execute(&out, container); err != nil {
			t.Fatal(err)
		}
		rt, err := decodeRuntime(out.Bytes(), nil, "selected")
		if err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		return rt
	}
	rt := render()
	st := rt.State
	if st == nil || st.Status != "exited" || !st.OOMKilled || st.ExitCode != 137 || st.RestartCount != 4 || st.Health != "" ||
		!st.StartedAt.Equal(time.Date(2026, 10, 16, 1, 2, 3, 5e8, time.UTC)) || !st.FinishedAt.Equal(time.Date(2026, 10, 16, 1, 4, 5, 0, time.UTC)) {
		t.Fatalf("the process state: %+v", st)
	}
	if rt.ID != "c0ffee" || rt.Running || rt.MemoryLimit != 268435456 || rt.Labels["forge.generation"] != "3" {
		t.Fatalf("identity and limit: %+v", rt)
	}
	container["State"].(map[string]any)["Health"] = map[string]any{"Status": "unhealthy", "Log": []any{map[string]any{"Output": "secret-bearing probe output"}}}
	if rt := render(); rt.State.Health != "unhealthy" {
		t.Fatalf("a configured health check: %+v", rt.State)
	}
	if strings.Contains(RuntimeFormat, "Log") || strings.Contains(RuntimeFormat, "Env") {
		t.Fatal("the format reads a health check's output or the environment")
	}
	rt, err := decodeRuntime([]byte(`{"id":"c0ffee","running":false}`), nil, "selected")
	if err != nil || rt.State != nil {
		t.Fatalf("an answer without state: %+v %v", rt, err)
	}
}
