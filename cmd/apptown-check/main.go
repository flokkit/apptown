// apptown-check judges a tree against a file of declared checks and prints what
// it found, as JSON. It links nothing of the forge: it is the proof that the
// judge is a library (internal/check), and it is the self-service run — what
// an agent can ask of its own work before anyone else looks at it.
//
//	apptown-check -tree . -checks checks.yaml
//	apptown-check -tree . -checks checks.json -complete ./ask-a-model -app http://127.0.0.1:8080
//	apptown-check -tree . -checks checks.yaml -scanners scanners.yaml -image myapp:local
//
// Exit 0 when everything it could judge conforms, 1 when something does not,
// 2 when it could not be run at all. What it could not assess — a rubric with
// no model, a probe with no app, a tool check with no scanner catalogue or no
// built image — is said in the output and does not fail the run.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	yamlv3 "gopkg.in/yaml.v3"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
)

// The program's one clock is the real one, taken here and passed down.
func main() {
	ctx := context.Background()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, execution.Docker, clock.Or(nil)))
}

func run(ctx context.Context, args []string, out, errOut io.Writer, backend string, now func() time.Time) int {
	fs := flag.NewFlagSet("apptown-check", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, "Experimental: not promised in 1.x; it exists to show the check library stands alone (stock/docs/guides/compatibility.md).")
		fmt.Fprintln(errOut, "usage of apptown-check:")
		fs.PrintDefaults()
	}
	tree := fs.String("tree", ".", "the tree to judge")
	checksPath := fs.String("checks", "", "a file of declared checks (JSON or YAML)")
	block := fs.String("block", "local", "the name the checks are declared under (part of every fingerprint)")
	version := fs.Int("version", 1, "the version they are declared at")
	complete := fs.String("complete", "", "a command that answers a checklist on its stdin; without it, rubrics are cannot-assess")
	app := fs.String("app", "", "base URL of a booted app for runtime probes; without it, probes are cannot-assess")
	naked := fs.String("naked", "", "the same app by address, with no front-door signal (default: the -app URL)")
	diff := fs.String("diff", "", "a file holding the change's diff, for diff-scoped rubrics")
	image := fs.String("image", "", "the app's built image, for tool checks that run inside it; without it, they are cannot-assess")
	scanners := fs.String("scanners", "", "a file of scanner manifests (JSON or YAML) tool checks name; without it, tool checks are cannot-assess")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *checksPath == "" {
		fmt.Fprintln(errOut, "apptown-check: -checks names the file of declared checks")
		return 2
	}
	declared, err := readChecks(*checksPath, check.Identity{Namespace: *block, Version: *version})
	if err != nil {
		fmt.Fprintf(errOut, "apptown-check: %v\n", err)
		return 2
	}
	src := check.Source{Tree: *tree, App: *app, Naked: *naked, Image: *image}
	if *diff != "" {
		b, err := os.ReadFile(*diff)
		if err != nil {
			fmt.Fprintf(errOut, "apptown-check: reading the diff: %v\n", err)
			return 2
		}
		src.Diff = string(b)
	}
	judge := &check.Judge{
		Run: func(req execution.Request) execution.Outcome {
			return execution.Runner{}.Run(ctx, backend, req)
		},
		Probe:      check.HTTPProber,
		Readiness:  declaredReadiness,
		NamePrefix: "apptown-check",
		Now:        now,
	}
	if *complete != "" {
		judge.Complete = commandCompleter(*complete)
	}
	if *scanners != "" {
		catalogue, err := readScanners(*scanners)
		if err != nil {
			fmt.Fprintf(errOut, "apptown-check: %v\n", err)
			return 2
		}
		judge.Scanners = catalogue
		judge.RunImage = func(image string, command, env []string) (int, string) { return runInImage(ctx, image, command, env) }
	}
	evaluations, err := judge.Evaluate(ctx, declared, src, check.Gate)
	if err != nil {
		fmt.Fprintf(errOut, "apptown-check: %v\n", err)
		return 2
	}
	return report(out, evaluations, now)
}

// readChecks reads a bare list of declarations, or an object with a `checks`
// list (and a block and version of its own), in JSON or YAML. The declaration
// is the library's, validated by it.
func readChecks(path string, id check.Identity) ([]check.Declared, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Block   string              `json:"block" yaml:"block"`
		Version int                 `json:"version" yaml:"version"`
		Checks  []check.Declaration `json:"checks" yaml:"checks"`
	}
	var list []check.Declaration
	switch {
	case json.Unmarshal(raw, &list) == nil && len(list) > 0:
	case json.Unmarshal(raw, &doc) == nil && len(doc.Checks) > 0:
		list = doc.Checks
	case yamlv3.Unmarshal(raw, &doc) == nil && len(doc.Checks) > 0:
		list = doc.Checks
	case yamlv3.Unmarshal(raw, &list) == nil && len(list) > 0:
	default:
		return nil, fmt.Errorf("%s declares no checks the reader understands", path)
	}
	if doc.Block != "" {
		id.Namespace = doc.Block
	}
	if doc.Version != 0 {
		id.Version = doc.Version
	}
	if err := check.Validate(list); err != nil {
		return nil, err
	}
	out := make([]check.Declared, 0, len(list))
	for _, d := range list {
		out = append(out, check.Declared{Identity: id, Declaration: d})
	}
	return out, nil
}

// readScanners reads a bare list of scanner manifests, or an object with a
// `scanners` list (the shape a forge seeds its own bindings from), in JSON or
// YAML. A tool check names one of them.
func readScanners(path string) (check.ScannersFunc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Scanners []check.Scanner `json:"scanners" yaml:"scanners"`
	}
	var list []check.Scanner
	switch {
	case json.Unmarshal(raw, &list) == nil && len(list) > 0:
	case json.Unmarshal(raw, &doc) == nil && len(doc.Scanners) > 0:
		list = doc.Scanners
	case yamlv3.Unmarshal(raw, &doc) == nil && len(doc.Scanners) > 0:
		list = doc.Scanners
	case yamlv3.Unmarshal(raw, &list) == nil && len(list) > 0:
	default:
		return nil, fmt.Errorf("%s declares no scanners the reader understands", path)
	}
	by := map[string]check.Scanner{}
	for _, s := range list {
		by[s.Name] = s
	}
	return func(name string) (check.Scanner, bool) { s, ok := by[name]; return s, ok }, nil
}

// runInImage runs a tool's command inside the app's built image, with nothing
// mounted and no network: the exec-image check sees the app as the app sees
// itself. The environment is the image's own — this command invents none.
func runInImage(ctx context.Context, image string, command, env []string) (exitCode int, output string) {
	argv := []string{"run", "--rm", "--network", "none"}
	for _, e := range env {
		argv = append(argv, "-e", e)
	}
	argv = append(append(argv, image), command...)
	out, err := (docker.CLI{}).Run(ctx, "", docker.Command{Args: argv})
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	}
	if err != nil {
		return -1, string(out)
	}
	return 0, string(out)
}

// commandCompleter answers a checklist by running a command with the prompt on
// its stdin and taking its stdout as the answer.
func commandCompleter(command string) check.Completer {
	return check.CompleterFunc(func(ctx context.Context, system, user string) (check.Completion, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Stdin = strings.NewReader(system + "\n\n" + user)
		cmd.Stderr = os.Stderr
		answer, err := cmd.Output()
		if err != nil {
			return check.Completion{}, err
		}
		return check.Completion{Text: string(answer), Model: command}, nil
	})
}

// declaredReadiness reads the readiness path the tree's own contract declares
// for its public part.
func declaredReadiness(src check.Source) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(src.Tree, ".apptown", "app.yaml"))
	if err != nil {
		return "", false
	}
	c, err := appcontract.Parse(raw)
	if err != nil || c == nil {
		return "", false
	}
	p := c.PublicPart()
	if p == nil || p.Intent == nil || p.Intent.Readiness == nil {
		return "", false
	}
	return p.Intent.Readiness.HTTP, p.Intent.Readiness.HTTP != ""
}

// evaluationJSON is what the command prints: one object per check, and every
// finding underneath, so another program can read the run.
type evaluationJSON struct {
	Block        string   `json:"block"`
	Version      int      `json:"version"`
	Check        string   `json:"check"`
	Evaluator    string   `json:"evaluator"`
	Outcome      string   `json:"outcome"`
	Detail       string   `json:"detail,omitempty"`
	ExitCode     *int     `json:"exitCode,omitempty"`
	DurationMs   int64    `json:"durationMs"`
	Output       string   `json:"output,omitempty"`
	Occurrences  []string `json:"occurrences,omitempty"`
	EvaluatorVer string   `json:"evaluatorVer,omitempty"`
	EvidenceHash string   `json:"evidenceHash,omitempty"`
	Files        []string `json:"files,omitempty"`
}

func report(out io.Writer, evaluations []check.Evaluation, now func() time.Time) int {
	rows := make([]evaluationJSON, 0, len(evaluations))
	findings := []check.Finding{}
	counts := map[string]int{}
	failed := false
	for _, ev := range evaluations {
		counts[ev.Outcome.String()]++
		if ev.Outcome == check.DoesNotConform || ev.Outcome == check.Error {
			failed = true
		}
		rows = append(rows, evaluationJSON{
			Block: ev.Namespace, Version: ev.Version, Check: ev.Name,
			Evaluator: check.Evaluator(ev.Declaration), Outcome: ev.Outcome.String(), Detail: ev.Detail,
			ExitCode: ev.Evidence.ExitCode, DurationMs: ev.Evidence.Duration.Milliseconds(),
			Output: ev.Evidence.Output, Occurrences: ev.Occurrences,
			EvaluatorVer: ev.Evidence.EvaluatorVersion, EvidenceHash: ev.Evidence.Hash, Files: ev.Evidence.Files,
		})
		findings = append(findings, ev.Findings...)
	}
	body, err := json.MarshalIndent(map[string]any{
		"at": now().UTC().Format(time.RFC3339), "evaluations": rows,
		"findings": findings, "summary": counts,
	}, "", " ")
	if err != nil {
		return 2
	}
	fmt.Fprintln(out, string(body))
	if failed {
		return 1
	}
	return 0
}
