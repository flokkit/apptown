package check

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/flokkit/apptown/internal/strs"
)

// AI-judged checks (design/subsystems/conformance-surface.md §2.2, §9): a rubric is judged
// on an EVIDENCE BUNDLE — for a tree rubric the files it names, packed
// deterministically from the committed tree; for a diff rubric the change's
// diff. Every rubric in a batch compiles into ONE checklist call.
//
// What is promised is the same QUESTION, never the same answer: a verdict
// records the packing version, the prompt template hash, the rubric+files
// hash (together the evaluator identity), the hash of the exact bundle it
// judged, and the model identifier the provider actually served.

// PackingVersion changes whenever the packing algorithm below changes —
// exclusions, caps, ordering, truncation markers. It is part of every AI
// verdict's evaluator identity.
const PackingVersion = "pack-v2"

// Caps (§9): per file, per rubric, per batch bundle.
const (
	fileCap   = 12 * 1024
	rubricCap = 48 * 1024
	batchCap  = 160 * 1024
)

// checklistSystem is the prompt template. Its hash is part of the evaluator
// identity, so an edited template is a changed evaluator.
const checklistSystem = `You verify software against a numbered conformance checklist.
Each item names its EVIDENCE: either FILES from the committed tree (judge the files as they stand)
or the DIFF of one change (judge only what the diff shows; unchanged code is out of scope).
Respond with ONLY a JSON array, one object per item, in order:
[{"item": 1, "verdict": "pass"|"fail"|"unclear", "evidence": "file:line or short quote", "note": "one sentence"}]
"fail" requires concrete cited evidence. When the evidence gives no basis to judge an item,
use "unclear", never "fail".`

func TemplateHash() string {
	h := sha256.Sum256([]byte(checklistSystem))
	return hex.EncodeToString(h[:4])
}

// LLMEvaluatorVersion is the evaluator identity of an AI check: packing version,
// template hash, and the hash of the rubric and its file globs. A changed
// rubric, file list, template or packing is a changed evaluator.
func LLMEvaluatorVersion(c Declaration) string {
	h := sha256.Sum256([]byte(c.Rubric + "\x00" + strings.Join(c.Files, "\x00")))
	return fmt.Sprintf("llm/%s/%s/%s", PackingVersion, TemplateHash(), hex.EncodeToString(h[:4]))
}

// --- evidence packing ---------------------------------------------------------

// skipDirs are never walked for a glob: dependencies, build output and the
// AppTown's own runtime state (.apptown holds agent sessions and staged reports).
// A file a check names by its exact path is still evidence wherever it lives
// (namedFiles): the runtime contract is .apptown/app.yaml, and a rubric that
// asks whether it is committed must see it, or it reports a false "missing
// app.yaml".
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	".venv": true, "__pycache__": true, ".apptown": true, ".forge": true, ".next": true, "target": true,
}

// globMatch matches a check's file glob against a slash-separated relative
// path. "**" matches any number of directories anywhere in the pattern, and a
// "*" never crosses a "/". A malformed pattern matches nothing.
func globMatch(pattern, rel string) bool {
	ok, _ := doublestar.Match(pattern, rel)
	return ok
}

func looksBinary(b []byte) bool {
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

func isMinified(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	return strings.Contains(base, ".min.") || strings.HasSuffix(base, ".map")
}

// packEvidence collects the files matching globs under src, deterministically
// (sorted relative paths; excluded dirs, except files a glob names exactly,
// symlinks, binaries and minified files skipped; per-file and per-rubric caps with explicit markers), and returns
// the bundle text plus the list of files it contains. A file or directory
// that cannot be read is the error: a bundle without it would be judged as
// if it were the whole evidence.
func packEvidence(src string, globs []string) (bundle string, files []string, err error) {
	var matched []string
	walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil // symlinks and specials are never evidence
		}
		if isMinified(rel) {
			return nil
		}
		for _, g := range globs {
			if globMatch(g, rel) {
				matched = append(matched, rel)
				break
			}
		}
		return nil
	})
	if walkErr != nil {
		return "", nil, fmt.Errorf("reading the evidence: %w", walkErr)
	}
	named, err := namedFiles(src, globs)
	if err != nil {
		return "", nil, err
	}
	for _, rel := range named {
		if !slices.Contains(matched, rel) {
			matched = append(matched, rel)
		}
	}
	sort.Strings(matched)

	var sb strings.Builder
	total := 0
	for i, rel := range matched {
		if total >= rubricCap {
			fmt.Fprintf(&sb, "[%d more matching file(s) omitted: rubric evidence cap]\n", len(matched)-i)
			break
		}
		b, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			return "", nil, fmt.Errorf("reading the evidence: %w", err)
		}
		if looksBinary(b) {
			continue
		}
		body := string(b)
		if len(body) > fileCap {
			body = body[:fileCap] + fmt.Sprintf("\n… [truncated: %d more bytes]\n", len(b)-fileCap)
		}
		fmt.Fprintf(&sb, "=== %s ===\n%s\n", rel, body)
		files = append(files, rel)
		total = sb.Len()
	}
	if len(matched) == 0 {
		sb.WriteString("[no files matched: " + strings.Join(globs, ", ") + "]\n")
	}
	return sb.String(), files, nil
}

// namedFiles are the globs that name one exact path (no pattern characters)
// whose file exists under src as a regular file, not through a symlink. The walk
// skips whole directories; these are found by name instead.
func namedFiles(src string, globs []string) ([]string, error) {
	var out []string
	for _, g := range globs {
		rel := path.Clean(g)
		if strings.ContainsAny(g, "*?[{\\") || !fs.ValidPath(rel) || isMinified(rel) {
			continue
		}
		ok, err := regularWithin(src, rel)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, rel)
		}
	}
	return out, nil
}

// regularWithin: rel is a regular file under src reached through real
// directories only, so a symlinked directory never leads evidence outside
// the tree (the walk never follows one either).
// A path that is not there is no such file; one that cannot be read is the
// error.
func regularWithin(src, rel string) (bool, error) {
	at := src
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("reading the evidence: %w", err)
		}
		if i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return false, nil
		}
	}
	return true, nil
}

func BundleHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// --- the checklist call --------------------------------------------------------

// Item is one rubric with the evidence it is judged on.
type Item struct {
	Declared
	Scope    string // tree | diff
	Evidence string // packed files for tree; "" for diff (the shared diff is attached once)
	Files    []string
	Hash     string // hash of the exact evidence this item is judged on
	// Unreadable says why the item's evidence could not be read: it is not
	// put to the model, and its judgement is cannot-assess.
	Unreadable string
}

// Verdict is one item's parsed answer.
type Verdict struct {
	Item     int    `json:"item"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
	Note     string `json:"note"`
}

// Completion is one answer from the model, as the caller's adapter observed
// it. What it cost and whose budget it lands on stay with the caller.
type Completion struct {
	Text   string
	Model  string // the model the provider actually served
	Tokens tokens
	Cost   float64
}

// completer asks a model for one answer.
type completer interface {
	Complete(ctx context.Context, system, user string) (Completion, error)
}

// CompleterFunc adapts a function to Completer.
type CompleterFunc func(ctx context.Context, system, user string) (Completion, error)

func (f CompleterFunc) Complete(ctx context.Context, system, user string) (Completion, error) {
	return f(ctx, system, user)
}

// checklistMeta is what a call records about itself.
type checklistMeta struct {
	Served     string // the model the provider actually served
	PromptHash string
	Items      int
	Cost       float64
	Tokens     tokens
	ParseError bool
	Retried    bool // the answer was asked for twice: the first was not JSON
}

// Judgement is what one checklist answer means for the item it answered: the
// verdict words are the checklist's, the outcome is the judge's. answered says
// the checklist returned anything at all for this item — an item it passed
// over is cannot-assess, since silence is not a pass. meta, when given,
// records what the call cost and what prompt it was.
func Judgement(it Item, v Verdict, answered bool, meta *checklistMeta, mode Mode) Evaluation {
	ev := Evaluation{Declared: it.Declared, Mode: mode,
		Evidence: evidence{EvaluatorVersion: LLMEvaluatorVersion(it.Declaration), Hash: it.Hash, Files: it.Files}}
	if meta != nil {
		ev.Evidence.PromptHash, ev.Evidence.Tokens = meta.PromptHash, meta.Tokens
	}
	if it.Unreadable != "" {
		ev.Outcome, ev.Detail = CannotAssess, "its evidence could not be read: "+it.Unreadable
		return ev
	}
	if !answered {
		ev.Outcome, ev.Detail = CannotAssess, "no verdict returned for this item"
		return ev
	}
	ev.Detail = strings.TrimSpace(v.Note + " [" + v.Evidence + "]")
	switch v.Verdict {
	case "pass":
		ev.Outcome = Conforms
	case "fail":
		ev.Outcome = DoesNotConform
		ev.Findings = []Finding{conformanceFinding(it.Declared, ev.Detail, " (llm, findings-only)")}
	default:
		ev.Outcome = CannotAssess
	}
	return ev
}

// TreeItems packs every tree-scoped rubric's evidence from src, applying the
// batch-level cap in order (later rubrics lose their evidence first, marked).
func TreeItems(checks []Declared, src string) []Item {
	var items []Item
	total := 0
	for _, sc := range checks {
		if Evaluator(sc.Declaration) != "llm" || Scope(sc.Declaration) != ScopeTree {
			continue
		}
		ev, files, err := packEvidence(src, sc.Files)
		if err != nil {
			items = append(items, Item{Declared: sc, Scope: ScopeTree, Unreadable: err.Error()})
			continue
		}
		if total+len(ev) > batchCap {
			ev = "[evidence omitted: project bundle cap reached]\n"
			files = nil
		}
		total += len(ev)
		items = append(items, Item{Declared: sc, Scope: ScopeTree, Evidence: ev, Files: files, Hash: BundleHash(ev)})
	}
	return items
}

// DiffItems lists every diff-scoped rubric; they share one diff.
func DiffItems(checks []Declared, diff string) []Item {
	var items []Item
	h := BundleHash(diff)
	for _, sc := range checks {
		if Evaluator(sc.Declaration) == "llm" && Scope(sc.Declaration) == ScopeDiff {
			items = append(items, Item{Declared: sc, Scope: ScopeDiff, Hash: h})
		}
	}
	return items
}

// Checklist makes the ONE call for a set of items and returns verdicts by item
// number (1-based). diff is attached once when any item is diff-scoped. A
// failed call returns err; the caller records the items as skipped with the
// reason — conformance never depends on a model being up.
//
// An item whose evidence could not be read (Unreadable) is left out of the
// prompt, keeping every other item's number; with none left, no call is made.
func (j *Judge) Checklist(ctx context.Context, items []Item, diff string) (map[int]Verdict, *checklistMeta, error) {
	if !slices.ContainsFunc(items, func(it Item) bool { return it.Unreadable == "" }) {
		return nil, nil, nil
	}
	if j.Complete == nil {
		return nil, nil, fmt.Errorf("no model bound")
	}
	var sb strings.Builder
	sb.WriteString("CHECKLIST — items are numbered; each names its evidence.\n\n")
	var diffNums []string
	for i, it := range items {
		if it.Unreadable != "" {
			continue
		}
		src := "DIFF"
		if it.Scope == ScopeTree {
			src = "FILES for item " + fmt.Sprint(i+1)
		} else {
			diffNums = append(diffNums, fmt.Sprint(i+1))
		}
		fmt.Fprintf(&sb, "%d. [%s v%d · %s] %s\n   Rubric: %s\n   Evidence: %s\n",
			i+1, it.Namespace, it.Version, it.Name, it.Expects, it.Rubric, src)
	}
	for i, it := range items {
		if it.Scope == ScopeTree && it.Unreadable == "" {
			fmt.Fprintf(&sb, "\n--- FILES for item %d (%s) ---\n%s", i+1, strings.Join(it.Files, ", "), it.Evidence)
		}
	}
	if len(diffNums) > 0 {
		d := diff
		if strings.TrimSpace(d) == "" {
			d = "[no diff]"
		}
		if len(d) > 60_000 {
			d = d[:60_000] + "\n... (diff truncated for checklist)"
		}
		fmt.Fprintf(&sb, "\n--- DIFF (items %s) ---\n\n%s", strings.Join(diffNums, ", "), d)
	}
	user := sb.String()
	ph := sha256.Sum256([]byte(checklistSystem + user))
	meta := &checklistMeta{PromptHash: hex.EncodeToString(ph[:8]), Items: len(items)}

	c, err := j.Complete.Complete(ctx, checklistSystem, user)
	if err != nil {
		return nil, meta, fmt.Errorf("checklist model call failed: %s", strs.Truncate(err.Error(), 120))
	}
	meta.Served, meta.Cost, meta.Tokens = c.Model, c.Cost, c.Tokens
	vs, perr := parseChecklist(c.Text)
	if perr != nil {
		// One more ask before giving up: a model that answered in prose
		// once usually answers in JSON when told again, and one hiccup
		// should not cost every verdict in the batch. The receipt says the
		// answer was asked for twice.
		c2, err2 := j.Complete.Complete(ctx, checklistSystem, user+"\n\nYour previous answer was not a JSON array. Answer again with the JSON array only, nothing before or after it.")
		if err2 == nil {
			meta.Cost += c2.Cost
			meta.Tokens.Input += c2.Tokens.Input
			meta.Tokens.Output += c2.Tokens.Output
			meta.Retried = true
			vs, perr = parseChecklist(c2.Text)
		}
	}
	if perr != nil {
		meta.ParseError = true
		return nil, meta, fmt.Errorf("checklist response was not parseable JSON")
	}
	by := map[int]Verdict{}
	for _, v := range vs {
		by[v.Item] = v
	}
	return by, meta, nil
}

// parseChecklist reads the judge's answer as a JSON array of verdicts,
// tolerating a code fence around it.
func parseChecklist(out string) ([]Verdict, error) {
	clean := strings.TrimSpace(out)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	var vs []Verdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(clean)), &vs); err != nil {
		return nil, err
	}
	return vs, nil
}
