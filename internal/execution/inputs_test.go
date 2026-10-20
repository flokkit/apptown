package execution

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// An input's blob is the tar the worker's blob store takes: one regular
// entry named "content" holding the bytes with the input's mode, named by
// the tar's SHA-256; the same content makes the same blob.
func TestAnInputIsAContentAddressedBlob(t *testing.T) {
	t.Parallel()
	in, err := NewInput("/apptown/in/bin/apptown", []byte("#!/bin/sh\n"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(in.BlobTar())
	if in.Blob != hex.EncodeToString(sum[:]) {
		t.Fatalf("the blob is not named by its tar's digest: %s", in.Blob)
	}
	tr := tar.NewReader(bytes.NewReader(in.BlobTar()))
	hdr, err := tr.Next()
	if err != nil || hdr.Name != "content" || hdr.Typeflag != tar.TypeReg || hdr.Mode != 0o755 {
		t.Fatalf("the blob's entry: %+v %v", hdr, err)
	}
	if body, _ := io.ReadAll(tr); string(body) != "#!/bin/sh\n" {
		t.Fatalf("the blob's content: %q", body)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("the blob holds more than one entry: %v", err)
	}
	again, _ := NewInput("/apptown/in/elsewhere", []byte("#!/bin/sh\n"), 0o755)
	if again.Blob != in.Blob {
		t.Fatal("the same content made another blob")
	}
	other, _ := NewInput("/apptown/in/bin/apptown", []byte("#!/bin/sh\n"), 0o644)
	if other.Blob == in.Blob {
		t.Fatal("another mode made the same blob")
	}
}

// A request's identity covers its inputs' blobs, paths and modes, and its
// declared results, not the bytes carried beside them.
func TestARequestsDigestCoversItsInputsAndResults(t *testing.T) {
	t.Parallel()
	in, _ := NewInput("/apptown/in/steering.md", []byte("steer"), 0o644)
	base := TaskSpec{Key: "k", Image: "i", Argv: []string{"a"}, Admission: Admission{Profile: "p", Network: NetworkNone},
		Inputs: []Input{in}, Results: []Result{{Name: "verdict", Path: "/work/.forge/review.json", Max: 10, Fresh: true}}}
	named := base
	named.Inputs = []Input{{Blob: in.Blob, Path: in.Path, Mode: in.Mode}}
	if named.Digest() != base.Digest() {
		t.Fatal("the bytes carried beside an input changed the request's identity")
	}
	changes := map[string]func(*TaskSpec){
		"blob":  func(s *TaskSpec) { s.Inputs = []Input{{Blob: strings.Repeat("a", 64), Path: in.Path, Mode: in.Mode}} },
		"path":  func(s *TaskSpec) { s.Inputs = []Input{{Blob: in.Blob, Path: "/apptown/in/other.md", Mode: in.Mode}} },
		"mode":  func(s *TaskSpec) { s.Inputs = []Input{{Blob: in.Blob, Path: in.Path, Mode: 0o755}} },
		"fresh": func(s *TaskSpec) { s.Results = []Result{{Name: "verdict", Path: "/work/.forge/review.json", Max: 10}} },
		"max": func(s *TaskSpec) {
			s.Results = []Result{{Name: "verdict", Path: "/work/.forge/review.json", Max: 11, Fresh: true}}
		},
		"result": func(s *TaskSpec) { s.Results = nil },
	}
	for name, change := range changes {
		changed := base
		change(&changed)
		if changed.Digest() == base.Digest() {
			t.Errorf("a change of %s kept the request's identity", name)
		}
	}
}

func TestInvalidInputsAreRefused(t *testing.T) {
	t.Parallel()
	blob := strings.Repeat("ab", 32)
	ok := Input{Blob: blob, Path: "/apptown/in/a.md", Mode: 0o644}
	if err := ValidInputs([]Input{ok, {Blob: blob, Path: "/apptown/in/bin/apptown", Mode: 0o755}}); err != nil {
		t.Fatalf("valid inputs refused: %v", err)
	}
	many := make([]Input, MaxInputs+1)
	for i := range many {
		many[i] = Input{Blob: blob, Path: "/apptown/in/f" + strings.Repeat("x", i), Mode: 0o644}
	}
	over := func(n int, size int64) []Input {
		var out []Input
		for i := range n {
			out = append(out, Input{Blob: blob, Path: "/apptown/in/f" + strings.Repeat("x", i), Mode: 0o644, size: size})
		}
		return out
	}
	cases := map[string][]Input{
		"the inputs root itself": {{Blob: blob, Path: "/apptown/in", Mode: 0o644}},
		"outside /apptown/in":    {{Blob: blob, Path: "/work/a.md", Mode: 0o644}},
		"a sibling of the root":  {{Blob: blob, Path: "/forge/input/a", Mode: 0o644}},
		"a parent escape":        {{Blob: blob, Path: "/apptown/in/../../etc/passwd", Mode: 0o644}},
		"not clean":              {{Blob: blob, Path: "/apptown/in//a", Mode: 0o644}},
		"relative":               {{Blob: blob, Path: "forge/in/a", Mode: 0o644}},
		"a mount separator":      {{Blob: blob, Path: "/apptown/in/a:rw", Mode: 0o644}},
		"no blob":                {{Path: "/apptown/in/a", Mode: 0o644}},
		"not a digest":           {{Blob: "../x", Path: "/apptown/in/a", Mode: 0o644}},
		"another mode":           {{Blob: blob, Path: "/apptown/in/a", Mode: 0o600}},
		"a setuid mode":          {{Blob: blob, Path: "/apptown/in/a", Mode: 0o4755}},
		"twice at one path":      {ok, ok},
		"inside another input":   {ok, {Blob: blob, Path: "/apptown/in/a.md/b", Mode: 0o644}},
		"too many":               many,
		"one too large":          over(1, MaxInputBytes+1),
		"too large together":     over(9, 60<<20),
	}
	for name, inputs := range cases {
		if err := ValidInputs(inputs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewInput("/apptown/in/big", make([]byte, MaxInputBytes+1), 0o644); err == nil {
		t.Error("an input over the limit was prepared")
	}
	spec := TaskSpec{Key: "k", Image: "i", Argv: []string{"a"}, Admission: Admission{Profile: "p", Network: NetworkNone}, Inputs: cases["outside /apptown/in"]}
	if err := spec.Validate(time.Now()); err == nil {
		t.Error("a request with an invalid input was valid")
	}
}

func TestInvalidResultsAreRefused(t *testing.T) {
	t.Parallel()
	ok := []Result{{Name: "verdict", Path: "/work/.forge/review.json", Max: 256 << 10, Fresh: true},
		{Name: "transcript", Path: "/apptown/state/sessions/s.jsonl", Max: 32 << 20}}
	if err := ValidResults(ok); err != nil {
		t.Fatalf("valid results refused: %v", err)
	}
	many := make([]Result, MaxResults+1)
	for i := range many {
		many[i] = Result{Name: "r" + strings.Repeat("x", i), Path: "/work/r" + strings.Repeat("x", i), Max: 1}
	}
	cases := map[string][]Result{
		"the tree itself":       {{Name: "a", Path: "/work", Max: 1}},
		"the state itself":      {{Name: "a", Path: "/apptown/state", Max: 1}},
		"outside both":          {{Name: "a", Path: "/etc/passwd", Max: 1}},
		"an input":              {{Name: "a", Path: "/apptown/in/steering.md", Max: 1}},
		"a parent escape":       {{Name: "a", Path: "/work/../etc/passwd", Max: 1}},
		"a state escape":        {{Name: "a", Path: "/apptown/state/../in/x", Max: 1}},
		"relative":              {{Name: "a", Path: "work/a", Max: 1}},
		"no name":               {{Path: "/work/a", Max: 1}},
		"a name with a slash":   {{Name: "a/b", Path: "/work/a", Max: 1}},
		"no maximum":            {{Name: "a", Path: "/work/a"}},
		"a maximum over":        {{Name: "a", Path: "/work/a", Max: MaxResultBytes + 1}},
		"one name twice":        {{Name: "a", Path: "/work/a", Max: 1}, {Name: "a", Path: "/work/b", Max: 1}},
		"one path twice":        {{Name: "a", Path: "/work/a", Max: 1}, {Name: "b", Path: "/work/a", Max: 1}},
		"too many":              many,
		"too large together":    {{Name: "a", Path: "/work/a", Max: MaxResultBytes}, {Name: "b", Path: "/work/b", Max: MaxResultBytes}},
		"a mount separator":     {{Name: "a", Path: "/work/a:b", Max: 1}},
		"a sibling of the tree": {{Name: "a", Path: "/workshop/a", Max: 1}},
	}
	for name, results := range cases {
		if err := ValidResults(results); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Each captured state is whole: present carries its bytes, refused its
// reason, and nothing else carries content; an unknown state is not one.
func TestACapturedResultIsOneOfFourStates(t *testing.T) {
	t.Parallel()
	valid := []ResultFile{{State: ResultPresent, Content: []byte("x"), Size: 1}, {State: ResultPresent}, {State: ResultAbsent},
		{State: ResultRefused, Reason: RefusedSymlink}, {State: ResultTooLarge, Size: 99}}
	for _, f := range valid {
		if err := f.Valid(); err != nil {
			t.Errorf("%+v: %v", f, err)
		}
	}
	invalid := []ResultFile{{}, {State: "empty"}, {State: ResultPresent, Content: []byte("x")}, {State: ResultAbsent, Content: []byte("x")},
		{State: ResultRefused}, {State: ResultTooLarge, Content: []byte("x")}}
	for _, f := range invalid {
		if err := f.Valid(); err == nil {
			t.Errorf("%+v accepted", f)
		}
	}
}

// A backend that does not claim inputs or results refuses a request that
// has them, before creating anything; the worker claims both.
func TestInputsAndResultsNeedABackendThatClaimsThem(t *testing.T) {
	t.Parallel()
	in, _ := NewInput("/apptown/in/a", []byte("a"), 0o644)
	spec := TaskSpec{Key: "k", Image: "i", Argv: []string{"a"}, Admission: Admission{Profile: "p", Network: NetworkNone}}
	withInputs, withResults := spec, spec
	withInputs.Inputs = []Input{in}
	withResults.Results = []Result{{Name: "a", Path: "/work/a", Max: 1}}
	binary, calls := recordingDocker(t)
	for _, s := range []TaskSpec{withInputs, withResults} {
		if _, err := (DockerTasks{Binary: binary}).Ensure(context.Background(), s); !errors.Is(err, ErrUnsupported) {
			t.Errorf("the local driver: %v", err)
		}
		if err := (WorkerTasks{}).Capabilities().Unclaimed(s); err != nil {
			t.Errorf("the worker: %v", err)
		}
	}
	if err := (Capabilities{}).Unclaimed(spec); err != nil {
		t.Errorf("a plain request: %v", err)
	}
	if recorded, _ := os.ReadFile(calls); len(recorded) != 0 {
		t.Fatalf("the local driver called Docker: %s", recorded)
	}
}

// A request without inputs or results keeps the identity it had before
// they existed: a task ensured before, and retried after, is not another.
func TestARequestWithoutInputsKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	spec := TaskSpec{Key: "k", Image: "alpine:3", Argv: []string{"true"}, Env: []string{"A=1"},
		Admission: Admission{Profile: "p", Network: NetworkNone, Limits: Limits{CPUs: 1}},
		Files:     []File{{Path: "/etc/x", Content: []byte("x"), Mode: 0o644}}}
	// Computed by the code before inputs and results were added.
	const before = "61acef033abbae765d1b9e82a86c412cb2b906afd7cb8dec9697ce7a8b16f43d"
	if got := spec.Digest(); got != before {
		t.Fatalf("the identity changed: %s", got)
	}
}
