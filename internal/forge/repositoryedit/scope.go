package repositoryedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
)

// Scope of a remediation (the problem is the process, not the change): a
// change that exists to close one conformance check has no
// business moving the app's dependencies. Quick Poll's readiness fix ran
// `bundle update bootsnap` because the gem was merely outdated, and the
// lockfile bump rode into a one-line config change. Steering says not to;
// this is the deterministic guard behind the steering: the gate lists the
// dependency files a remediation touched and refuses them as unmet
// acceptance, so the implementer is sent back with the file names, and
// the reviewer is told the same.

// dependencyFiles names the files whose change means "the dependencies
// moved", by ecosystem. Basenames, matched anywhere in the tree.
var dependencyFiles = map[string]bool{
	"Gemfile.lock": true, "Gemfile": true,
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "package.json": true,
	"go.mod": true, "go.sum": true,
	"requirements.txt": true, "poetry.lock": true, "Pipfile.lock": true, "uv.lock": true, "pyproject.toml": true,
	"Cargo.lock": true, "Cargo.toml": true,
	"composer.lock": true, "composer.json": true,
	"packages.lock.json": true,
	"mise.toml":          true,
}

// ChangedFile is one file a change touched. Additive is true when the
// change only ADDED lines to it: every line it removed reappears among the
// lines it added (trailing comma and whitespace aside, so appending an
// entry to a JSON object counts). A remediation may add a package the fix
// needs (an admin page that needs whitenoise, say); it may not bump, replace or remove what was already there. Ignored is
// true when the repository's own ignore rules cover the path: build output
// or AppTown runtime files the project itself declared not to be source.
type ChangedFile struct {
	Path     string
	Additive bool
	Ignored  bool
	Added    bool // the change created it; it did not exist on the base
}

// IgnoredChanges lists the files a change committed that the repository
// ignores. What counts as build output depends on the language and the
// tools, so AppTown holds no list of folder names: the project's
// .gitignore is the declaration and git's matcher is the reference (a
// tracked target/ can be in the project's own .gitignore).
// This holds for every change, not only remediations.
func IgnoredChanges(changed []ChangedFile) []string {
	var out []string
	for _, c := range changed {
		if c.Ignored {
			out = append(out, c.Path)
		}
	}
	return out
}

// DependencyChangesOutOfScope returns the dependency files a remediation
// with this target did more than add to, unless the target itself is about
// dependencies or the toolchain. An ordinary change may move its
// dependencies; a change that exists to close one check may not.
func DependencyChangesOutOfScope(changed []ChangedFile, target workflow.AcceptanceScope) []string {
	if len(target.Keys()) == 0 {
		return nil
	}
	if targetIsAboutDependencies(target) {
		return nil
	}
	var out []string
	for _, c := range changed {
		if !c.Additive && isDependencyFile(c.Path) {
			out = append(out, c.Path)
		}
	}
	return out
}

func isDependencyFile(f string) bool {
	return dependencyFiles[path.Base(f)] || strings.HasSuffix(f, ".csproj") || strings.HasSuffix(f, ".gemspec")
}

// additiveDiff reads a unified diff (no context) and reports whether
// everything that was there survives: each removed line comes back as an
// added line that carries all of its tokens — the same line, the same line
// with a trailing comma, or the same list with one more entry on it
// (a one-line pyproject dependencies list, say). A bump
// changes a token and a removal brings nothing back, so both fail.
func additiveDiff(diff string) bool {
	var added [][]string
	var removed [][]string
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added = append(added, lineTokens(l[1:]))
		case strings.HasPrefix(l, "-"):
			removed = append(removed, lineTokens(l[1:]))
		}
	}
	used := make([]bool, len(added))
	for _, r := range removed {
		found := false
		for i, a := range added {
			if !used[i] && tokensWithin(r, a) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// lineTokens splits a line into its words: whitespace, commas and brackets
// separate, everything else (quotes, ==, versions) stays inside a token.
func lineTokens(l string) []string {
	return strings.FieldsFunc(l, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '[' || r == ']' || r == '{' || r == '}'
	})
}

// tokensWithin reports whether every token of need occurs in have, with
// multiplicity.
func tokensWithin(need, have []string) bool {
	count := map[string]int{}
	for _, t := range have {
		count[t]++
	}
	for _, t := range need {
		if count[t] == 0 {
			return false
		}
		count[t]--
	}
	return true
}

// ChangedFilesSince lists the files a workspace's HEAD changed against a
// base commit, saying of each dependency file whether it only gained
// lines and of every file whether the repository ignores it; empty when
// the base is unknown. A diff or ignore question git cannot answer is an
// error, never an empty list: the scope guard reads "nothing changed" as
// "nothing to refuse", so a read failure must not look like one.
func ChangedFilesSince(ctx context.Context, ws, base string) ([]ChangedFile, error) {
	if base == "" {
		return nil, nil
	}
	return changedFiles(folderGit(ctx, ws), base, "HEAD", func(present []ChangedFile) (map[string]bool, error) { return ignoredPaths(ctx, ws, present) })
}

// ChangedFilesBetween is ChangedFilesSince for an exact revision: what rev
// changed against base in repo, with the ignore question answered by the
// ignore rules of tree, a checkout of rev. A landing uses it for its pinned
// candidate: neither the workspace's HEAD nor its live .gitignore can stand
// in for the commit being checked. Errors are returned, never an empty list.
func ChangedFilesBetween(ctx context.Context, repo, base, rev, tree string) ([]ChangedFile, error) {
	if base == "" {
		return nil, nil
	}
	return changedFiles(folderGit(ctx, repo), base, rev, func(present []ChangedFile) (map[string]bool, error) { return ignoredInTree(ctx, tree, present) })
}

// ChangedFilesInHub is ChangedFilesBetween for a result that has no folder
// (a task's candidate on a worker): what rev changed against base in the
// project's hub, with the ignore question answered by the .gitignore files
// at rev. Every command runs hardened in the hub, with no external diff or
// textconv. A base the change does not record, one the hub lacks, a diff
// or ignore rules that cannot be read are errors, never an empty list.
func ChangedFilesInHub(ctx context.Context, hub, base, rev string) ([]ChangedFile, error) {
	if base == "" {
		return nil, errors.New("the change records no base commit")
	}
	git := func(args ...string) ([]byte, error) {
		return gitexec.HubOutput(ctx, hub, nil, append([]string{args[0], "--no-ext-diff", "--no-textconv"}, args[1:]...)...)
	}
	return changedFiles(git, base, rev, func(present []ChangedFile) (map[string]bool, error) { return ignoredAtRev(ctx, hub, rev, present) })
}

// folderGit runs git in a folder or repository, as the scope readers
// always have, keeping stdout apart: the answer is paths, and git's hints
// on stderr are not.
func folderGit(ctx context.Context, dir string) func(...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
		return gitexec.OutputBytes(ctx, dir, nil, args...)
	}
}

// gitFailure is err with what git said on stderr, when it said anything.
func gitFailure(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(exit.Stderr))
	}
	return err
}

// changedFiles reads the whole change, base to rev. Paths are exchanged
// with git NUL-delimited, never as lines or words: a legal path may hold
// spaces, tabs, quotes, newlines or non-ASCII, and the guard must judge and
// name the path that was committed, not a fragment or git's quoted form
// (a force-added "target/out file.class" was once judged as "file.class").
// Rename detection is off, so every entry is
// one status and one path: a rename is its deletion and its addition.
func changedFiles(git func(...string) ([]byte, error), base, rev string, ignored func([]ChangedFile) (map[string]bool, error)) ([]ChangedFile, error) {
	out, err := git("diff", "--name-status", "-z", "--no-renames", base+".."+rev, "--")
	if err != nil {
		return nil, fmt.Errorf("diffing %s..%s: %w", base, rev, gitFailure(err))
	}
	fields := strings.Split(string(out), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("diffing %s..%s: git's list of changed files is incomplete", base, rev)
	}
	var files []ChangedFile
	var present []ChangedFile // still in the tree at rev: the ignore question is theirs
	for i := 0; i < len(fields); i += 2 {
		status, p := fields[i], fields[i+1]
		if status == "" || p == "" || strings.ContainsAny(status[:1], "RC") {
			return nil, fmt.Errorf("diffing %s..%s: unexpected entry %q %q", base, rev, status, p)
		}
		c := ChangedFile{Path: p, Added: strings.HasPrefix(status, "A")}
		if isDependencyFile(p) {
			d, err := git("diff", "--no-renames", "-U0", base+".."+rev, "--", ":(literal)"+p)
			if err != nil {
				return nil, fmt.Errorf("diffing %s: %w", p, gitFailure(err))
			}
			c.Additive = additiveDiff(string(d))
		}
		files = append(files, c)
		// A deletion is the fix, not the fault: removing a tracked
		// .apptown/compose.yaml is exactly what the check asks for, and must
		// not be refused.
		if !strings.HasPrefix(status, "D") {
			present = append(present, c)
		}
	}
	which, err := ignored(present)
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].Ignored = which[files[i].Path]
	}
	return files, nil
}

// checkIgnore asks git's matcher which of these paths its ignore rules
// cover — tracked or not (--no-index), so a force-added file is still
// reported. Paths go in and come back NUL-delimited and unquoted, exactly
// as committed. Exit status 1 means nothing is ignored; any other failure
// is an error, because "could not ask" is not "nothing ignored".
func checkIgnore(run func(stdin io.Reader, args ...string) ([]byte, error), files []ChangedFile) (map[string]bool, error) {
	var in bytes.Buffer
	for _, f := range files {
		in.WriteString(f.Path)
		in.WriteByte(0)
	}
	raw, err := run(&in, "check-ignore", "--no-index", "-z", "--stdin")
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) { // 1: nothing ignored
		return nil, gitFailure(err)
	}
	set := map[string]bool{}
	for _, p := range strings.Split(string(raw), "\x00") {
		if p != "" {
			set[p] = true
		}
	}
	return set, nil
}

// ignoredInTree answers the ignore question from a checkout's own .gitignore
// files: a throwaway repository is made inside the disposable tree so git
// can read them, without the workspace's live rules.
func ignoredInTree(ctx context.Context, tree string, files []ChangedFile) (map[string]bool, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if out, err := gitexec.Run(ctx, tree, "init", "-q"); err != nil {
		return nil, fmt.Errorf("reading the checked tree's ignore rules: %w: %s", err, out)
	}
	set, err := checkIgnore(func(stdin io.Reader, args ...string) ([]byte, error) {
		return gitexec.OutputBytes(ctx, tree, stdin, args...)
	}, files)
	if err != nil {
		return nil, fmt.Errorf("reading the checked tree's ignore rules: %w", err)
	}
	return set, nil
}

// ignoredAtRev answers the ignore question from the .gitignore files at
// rev in the hub: each (a regular file, as Git reads them) is written into
// a throwaway folder, where Git reads them with no other rules. One that
// cannot be listed or read is an error: "could not ask" is not "nothing
// ignored".
func ignoredAtRev(ctx context.Context, hub, rev string, files []ChangedFile) (map[string]bool, error) {
	if len(files) == 0 {
		return nil, nil
	}
	listing, err := gitexec.HubOutput(ctx, hub, nil, "ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, fmt.Errorf("listing the ignore rules at %s: %w", rev, err)
	}
	tree, err := os.MkdirTemp("", "forge-ignore-rules-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tree)
	for _, entry := range bytes.Split(listing, []byte{0}) {
		meta, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(meta)
		if !ok || path.Base(name) != ".gitignore" || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			continue // Git reads no linked .gitignore either
		}
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return nil, fmt.Errorf("the ignore rules at %q are not in the tree", name)
		}
		body, err := gitexec.HubOutput(ctx, hub, nil, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, fmt.Errorf("reading the ignore rules %s at %s: %w", name, rev, err)
		}
		at := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(at, body, 0o644); err != nil {
			return nil, err
		}
	}
	if out, err := gitexec.HubRun(ctx, tree, "init", "-q"); err != nil {
		return nil, fmt.Errorf("reading the ignore rules at %s: %w: %s", rev, err, out)
	}
	set, err := checkIgnore(func(stdin io.Reader, args ...string) ([]byte, error) {
		return gitexec.HubOutput(ctx, tree, stdin, args...)
	}, files)
	if err != nil {
		return nil, fmt.Errorf("reading the ignore rules at %s: %w", rev, err)
	}
	return set, nil
}

// ignoredPaths asks the workspace's own ignore rules.
func ignoredPaths(ctx context.Context, ws string, files []ChangedFile) (map[string]bool, error) {
	if len(files) == 0 {
		return nil, nil
	}
	set, err := checkIgnore(func(stdin io.Reader, args ...string) ([]byte, error) {
		return gitexec.OutputBytes(ctx, ws, stdin, args...)
	}, files)
	if err != nil {
		return nil, fmt.Errorf("reading the workspace's ignore rules: %w", err)
	}
	return set, nil
}

func targetIsAboutDependencies(t workflow.AcceptanceScope) bool {
	about := func(block, check string) bool {
		return strings.HasPrefix(block, "deps/") || strings.HasPrefix(block, "build/") ||
			strings.Contains(check, "depend") || strings.Contains(check, "toolchain") || strings.Contains(check, "lockfile")
	}

	for _, k := range t.Keys() {
		parts := strings.SplitN(k, "|", 2)
		if len(parts) == 2 && about(parts[0], parts[1]) {
			return true
		}
	}
	return false
}

// reportArtifacts are the files a lane asks the agent to write as its
// report; a NEW one is the claim, not a change to be claimed. One that was
// already tracked on the base is the project's file (letterbox keeps its
// build report at .apptown/REPORT.md), and rewriting it is a change like any
// other.
//
// Commits from before the AppTown rename hold the same reports under .forge,
// and history does not change: export still reads them as prose.
var reportArtifacts = map[string]bool{".apptown/CHANGE.md": true, ".apptown/REPORT.md": true, ".apptown/VERIFY.md": true, ".apptown/review.json": true,
	".forge/CHANGE.md": true, ".forge/REPORT.md": true, ".forge/VERIFY.md": true, ".forge/review.json": true}

// ReportArtifact reports whether path is one of the files a lane asks the
// agent to write as its report: prose AppTown collects, never the agent's
// runtime state.
func ReportArtifact(path string) bool { return reportArtifacts[path] }

// UnclaimedChanges lists the changed files the implementer's report never
// names — by path, by basename, or by naming a directory that holds them.
// The lane's prompt says the report must account for EVERY changed file; a
// reviewer model missed a rewritten REPORT.md that the report did not
// mention, so the rule is
// deterministic. A generator (rails new, django-admin startproject, uv
// init) writes a hundred files the report can only claim as a tree, so a
// word naming a directory claims every changed file under it.
func UnclaimedChanges(changed []ChangedFile, claim string) []string {
	dirs := claimedDirectories(claim)
	var out []string
	for _, c := range changed {
		if reportArtifacts[c.Path] && c.Added {
			continue
		}
		if strings.Contains(claim, c.Path) || strings.Contains(claim, path.Base(c.Path)) {
			continue
		}
		if slices.ContainsFunc(dirs, func(d string) bool { return strings.HasPrefix(c.Path, d+"/") }) {
			continue
		}
		out = append(out, c.Path)
	}
	return out
}

// claimedDirectories reads every word of the report as a directory it
// may claim: "app/", "./config/", "app/models", and bare "bin" or
// "config" alike. Agents name generated folders without the slash ("bin,
// config and public are generated"). A word claims only the changed files
// under it, so one naming no folder of the change ("environments", a file
// name, ordinary prose) claims nothing. The root ("./", "/", ".") claims
// nothing either: changed paths are relative, so none is under "./" or
// "//", and a file at the top level, a dotfile included, is named itself.
func claimedDirectories(claim string) []string {
	var dirs []string
	for _, word := range strings.FieldsFunc(claim, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("`'\"*,;()[]<>|", r)
	}) {
		dirs = append(dirs, path.Clean(strings.TrimPrefix(strings.TrimRight(word, ".:"), "./")))
	}
	return dirs
}
