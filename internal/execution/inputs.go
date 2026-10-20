package execution

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Input is a file a task reads, read-only, at Path under /apptown/in/
// (design/operations/workers.md, "Agent and review steps on the worker").
// It is prepared on the forge as bytes and sent to
// the backend once as a content-addressed blob: a tar holding one regular
// entry named "content", named by the SHA-256 of the tar (the worker's
// PUT /v1/blobs/{sum}, which chats use too). A request names the blob, never
// a path on either machine, so its identity covers the content.
type Input struct {
	Blob string      `json:"blob"` // SHA-256 of the blob's tar, hex
	Path string      `json:"path"` // absolute and clean, under /apptown/in/
	Mode fs.FileMode `json:"mode"` // 0644 or 0755
	// tar is the blob on the forge's side (NewInput); nil for an input that
	// names a blob the backend is expected to hold already. It is never part
	// of a request's identity: Blob is.
	tar  []byte
	size int64
}

// InputsRoot is where a task's inputs are presented.
const InputsRoot = "/apptown/in"

// StateRoot is where a workspace's own writable state is presented: beside
// the tree, never in it, and kept across the tasks that use the workspace.
const StateRoot = "/apptown/state"

// ToolCacheMount is where a container finds the shared download cache of
// the toolchains projects declare (mise's MISE_CACHE_DIR): the forge's
// forge-cache volume on its own host, a worker's tool cache on a worker.
// It holds what mise fetches (release listings, tool archives), never a
// tool's installation, which stays the container's own.
const ToolCacheMount = "/apptown-cache/mise-cache"

// Bounds on a request's inputs: enough for an agent's steering, resources,
// attachments and the forge CLI, never a way to send a tree.
const (
	MaxInputs          = 256
	MaxInputBytes      = 64 << 20  // one input
	MaxInputTotalBytes = 512 << 20 // all of a request's inputs
)

var blobPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// NewInput prepares content to be presented at path with mode, as an input
// that carries its blob for the driver to send.
func NewInput(path string, content []byte, mode fs.FileMode) (Input, error) {
	if len(content) > MaxInputBytes {
		return Input{}, fmt.Errorf("the input %s is %d bytes, over the %d limit", path, len(content), MaxInputBytes)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// No time or owner: the same content always makes the same blob.
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "content", Mode: int64(mode.Perm()), Size: int64(len(content)), Format: tar.FormatPAX}); err != nil {
		return Input{}, err
	}
	if _, err := tw.Write(content); err != nil {
		return Input{}, err
	}
	if err := tw.Close(); err != nil {
		return Input{}, err
	}
	sum := sha256.Sum256(buf.Bytes())
	in := Input{Blob: hex.EncodeToString(sum[:]), Path: path, Mode: mode, tar: buf.Bytes(), size: int64(len(content))}
	return in, ValidInputs([]Input{in})
}

// BlobTar is the blob to send for the input, or nil when the input names a
// blob without carrying it.
func (in Input) BlobTar() []byte { return in.tar }

// WithBlob returns the input carrying blob, its blob as BlobTar gave it
// (an admitted request's input read back from where the forge kept it). A
// blob that is not the one the input names, or not one entry, is refused.
func (in Input) WithBlob(blob []byte) (Input, error) {
	if sum := sha256.Sum256(blob); hex.EncodeToString(sum[:]) != in.Blob {
		return Input{}, fmt.Errorf("the kept blob for %s is not %s", in.Path, in.Blob)
	}
	tr := tar.NewReader(bytes.NewReader(blob))
	hdr, err := tr.Next()
	if err != nil || hdr.Typeflag != tar.TypeReg || hdr.Name != "content" {
		return Input{}, fmt.Errorf("the kept blob for %s is not one file", in.Path)
	}
	in.tar, in.size = blob, hdr.Size
	return in, ValidInputs([]Input{in})
}

// ValidInputs says whether inputs may be presented to a task: each a blob
// digest, at a clean path of its own under /apptown/in/ that is no other
// input's folder, with mode 0644 or 0755, within the bounds. Sizes are
// checked here for inputs carrying their content, and by the backend, which
// knows every blob's size, for all.
func ValidInputs(inputs []Input) error {
	if len(inputs) > MaxInputs {
		return fmt.Errorf("%d inputs, over the %d limit", len(inputs), MaxInputs)
	}
	seen := map[string]bool{}
	var total int64
	for _, in := range inputs {
		if err := validInputPath(in.Path); err != nil {
			return err
		}
		if !blobPattern.MatchString(in.Blob) {
			return fmt.Errorf("the input %s names no blob digest: %q", in.Path, in.Blob)
		}
		if in.Mode != 0o644 && in.Mode != 0o755 {
			return fmt.Errorf("the input %s has mode %v; an input is 0644 or 0755", in.Path, in.Mode)
		}
		if seen[in.Path] {
			return fmt.Errorf("two inputs at %s", in.Path)
		}
		seen[in.Path] = true
		if in.size > MaxInputBytes {
			return fmt.Errorf("the input %s is %d bytes, over the %d limit", in.Path, in.size, MaxInputBytes)
		}
		total += in.size
	}
	if total > MaxInputTotalBytes {
		return fmt.Errorf("the inputs are %d bytes, over the %d limit", total, MaxInputTotalBytes)
	}
	// A file cannot be presented where another input needs a folder.
	for p := range seen {
		for dir := path.Dir(p); dir != InputsRoot; dir = path.Dir(dir) {
			if seen[dir] {
				return fmt.Errorf("the input %s is inside the input %s", p, dir)
			}
		}
	}
	return nil
}

func validInputPath(p string) error {
	if !cleanUnder(p, InputsRoot) {
		return fmt.Errorf("an input's path must be clean and under %s/: %q", InputsRoot, p)
	}
	return nil
}

// cleanUnder: p is absolute, clean and strictly under root, with no ".."
// and nothing Docker's mount syntax would read as a separator.
func cleanUnder(p, root string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && strings.HasPrefix(p, root+"/") &&
		!strings.ContainsAny(p, ":,\x00\n") && !slices.Contains(strings.Split(p, "/"), "..")
}

// Result is a file a task declares it will leave (a review's verdict, a
// transcript): the backend reads it when the task's container exits and
// keeps it with the task, so what the forge reads is bound to the task that
// produced it. Path is in the tree (/work/...) or the workspace's state
// (/apptown/state/...).
type Result struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Max  int64  `json:"max"` // bytes; a larger file is ResultTooLarge
	// Fresh: any file at Path is removed before the task starts, so what is
	// read after it exits was left by this task.
	Fresh bool `json:"fresh,omitempty"`
}

// Bounds on a request's result files.
const (
	MaxResults          = 16
	MaxResultBytes      = 64 << 20 // one result's Max
	MaxResultTotalBytes = 96 << 20 // all of a request's Max together
)

var resultNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidResults says whether results may be declared: each named once, at a
// clean path of its own in the tree or the state folder, with a maximum
// size within the bounds.
func ValidResults(results []Result) error {
	if len(results) > MaxResults {
		return fmt.Errorf("%d result files, over the %d limit", len(results), MaxResults)
	}
	names, paths := map[string]bool{}, map[string]bool{}
	var total int64
	for _, r := range results {
		if !resultNamePattern.MatchString(r.Name) {
			return fmt.Errorf("invalid result name %q", r.Name)
		}
		if !cleanUnder(r.Path, "/work") && !cleanUnder(r.Path, StateRoot) {
			return fmt.Errorf("a result file's path must be clean and under /work/ or %s/: %q", StateRoot, r.Path)
		}
		if names[r.Name] || paths[r.Path] {
			return fmt.Errorf("the result %s (%s) is declared twice", r.Name, r.Path)
		}
		names[r.Name], paths[r.Path] = true, true
		if r.Max <= 0 || r.Max > MaxResultBytes {
			return fmt.Errorf("the result %s's maximum must be between 1 and %d bytes", r.Name, MaxResultBytes)
		}
		total += r.Max
	}
	if total > MaxResultTotalBytes {
		return fmt.Errorf("the result files may be %d bytes together, over the %d limit", total, MaxResultTotalBytes)
	}
	return nil
}

// ResultState is what became of a declared result file when its task
// exited. None is ever turned into another: absent is not empty.
type ResultState string

const (
	ResultPresent  ResultState = "present"   // a regular file, within its maximum: its bytes
	ResultAbsent   ResultState = "absent"    // nothing at the path
	ResultRefused  ResultState = "refused"   // something the worker will not read there (Reason)
	ResultTooLarge ResultState = "too-large" // a regular file over its maximum (Size)
)

// ResultFile is a captured result file.
type ResultFile struct {
	State   ResultState `json:"state"`
	Content []byte      `json:"content,omitempty"` // present only
	Reason  string      `json:"reason,omitempty"`  // refused only
	Size    int64       `json:"size,omitempty"`    // present and too large
}

// Reasons a result file is refused.
const (
	RefusedSymlink    = "symlink"     // a symbolic link at the path or on the way to it
	RefusedNotRegular = "not-regular" // a folder, device, socket or pipe
	RefusedEscape     = "escape"      // the path leaves the tree or state folder
)

// Valid says whether a captured result is one of the four states, whole.
func (f ResultFile) Valid() error {
	switch f.State {
	case ResultPresent:
		if int64(len(f.Content)) != f.Size {
			return fmt.Errorf("a present result of %d bytes holds %d", f.Size, len(f.Content))
		}
	case ResultAbsent:
	case ResultRefused:
		if f.Reason == "" {
			return errors.New("a refused result without its reason")
		}
	case ResultTooLarge:
	default:
		return fmt.Errorf("unknown result state %q", f.State)
	}
	if f.State != ResultPresent && len(f.Content) > 0 {
		return fmt.Errorf("a %s result carries content", f.State)
	}
	return nil
}

// AgentImage is a worker's agent image: its content-derived name and the
// labels its build gave it (the subagents and extension versions a step's
// preparation reads).
type AgentImage struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// ErrNoAgentImage: the worker has no agent image to report.
var ErrNoAgentImage = errors.New("the worker has no agent image")

// needs is the capabilities a request needs beyond the lifecycle.
func (s TaskSpec) needs() Capabilities {
	return Capabilities{Inputs: len(s.Inputs) > 0, Results: len(s.Results) > 0, Images: len(s.Images) > 0, PrivateDocker: s.Private != nil}
}

// Unclaimed refuses a request that needs a capability c does not claim,
// with ErrUnsupported. Nothing is created.
func (c Capabilities) Unclaimed(s TaskSpec) error {
	n := s.needs()
	switch {
	case n.Inputs && !c.Inputs:
		return fmt.Errorf("%w: this backend presents no inputs", ErrUnsupported)
	case n.Results && !c.Results:
		return fmt.Errorf("%w: this backend keeps no result files", ErrUnsupported)
	case n.Images && !c.Images:
		return fmt.Errorf("%w: this backend captures no images", ErrUnsupported)
	case n.PrivateDocker && !c.PrivateDocker:
		return fmt.Errorf("%w: this backend runs no private Docker", ErrUnsupported)
	}
	return nil
}
