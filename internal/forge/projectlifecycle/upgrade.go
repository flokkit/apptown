package projectlifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/trailer"
)

// The AppTown rename's upgrade: the one time AppTown rewrites a project's repository and adoption
// outside a Change. Each project's repository folder .forge/ becomes
// .apptown/ on its default branch and on its open changes' branches, refs
// under refs/forge/ move to refs/apptown/, and its adoption advances to the
// stock and library content that already speaks the new names. It is
// idempotent: a project already upgraded is reported unchanged.

// UpgradeWork is what the upgrade asks the change owner.
type UpgradeWork interface {
	OpenRunsEverywhere(ctx context.Context) ([]workflow.OpenRun, error)
	OpenChangeBranches(ctx context.Context, project ids.ID) (map[int64]string, error)
}

// UpgradeReport is what the upgrade did, project by project.
type UpgradeReport struct {
	Projects []UpgradedProject `json:"projects"`
	Library  []string          `json:"library,omitempty"` // custom blocks and workflows given a renamed version
}

// UpgradedProject is one project's part of the report. Renamed lists the
// branches given a rename commit; Refused, the branches left as they were
// and why; Refs, the refs moved; Adoption, the revision the project's
// adoption now stands at when the upgrade advanced it.
type UpgradedProject struct {
	Slug     string            `json:"slug"`
	Renamed  []string          `json:"renamed,omitempty"`
	Refused  map[string]string `json:"refused,omitempty"`
	Refs     int               `json:"refs,omitempty"`
	Adoption int64             `json:"adoption,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// ErrUpgradeRunsOpen refuses the upgrade while any run is open: a run works
// on a change branch the upgrade would rewrite.
type ErrUpgradeRunsOpen struct{ Runs []string }

func (e ErrUpgradeRunsOpen) Error() string {
	return fmt.Sprintf("finish or stop the open runs first (%s); the upgrade rewrites the branches they work on", strings.Join(e.Runs, ", "))
}
func (ErrUpgradeRunsOpen) FaultKind() fault.Kind { return fault.Conflict }
func (ErrUpgradeRunsOpen) FaultCode() string     { return "runs-open" }

const upgradeSubject = "apptown: rename .forge to .apptown (upgrade to 1.0)"

// UpgradeToAppTown runs the upgrade as by, an administrator.
func (s *Service) UpgradeToAppTown(ctx context.Context, by string, work UpgradeWork) (UpgradeReport, error) {
	var report UpgradeReport
	open, err := work.OpenRunsEverywhere(ctx)
	if err != nil {
		return report, err
	}
	if len(open) > 0 {
		runs := make([]string, 0, len(open))
		for _, r := range open {
			runs = append(runs, fmt.Sprintf("%s run %d", r.Project, r.Number))
		}
		return report, ErrUpgradeRunsOpen{Runs: runs}
	}
	if report.Library, err = s.renameLibrary(ctx, by); err != nil {
		return report, err
	}
	projects, err := s.projects.All(ctx)
	if err != nil {
		return report, err
	}
	for i := range projects {
		p := &projects[i]
		row := UpgradedProject{Slug: p.Slug}
		if err := s.upgradeRepository(ctx, p, by, work, &row); err != nil {
			row.Error = err.Error()
		}
		if rev, err := s.adoption.AdvanceToLatest(ctx, p.ID, "upgrade to 1.0", by); err != nil {
			row.Error = strings.TrimPrefix(row.Error+"; adoption: "+err.Error(), "; ")
		} else if rev != nil {
			row.Adoption = rev.Revision
		}
		report.Projects = append(report.Projects, row)
	}
	return report, nil
}

// upgradeRepository renames .forge on the project's default branch and open
// change branches and moves its refs, under the repository lock every
// change workspace takes before cloning.
func (s *Service) upgradeRepository(ctx context.Context, p *project.Project, by string, work UpgradeWork, row *UpgradedProject) error {
	if p.DefaultBranch == "" {
		return nil
	}
	dir := hubDir(s.dataDir, p.Slug)
	if !gitexec.IsRepo(dir) {
		return nil
	}
	changes, err := work.OpenChangeBranches(ctx, p.ID)
	if err != nil {
		return err
	}
	release, err := s.repositories.Acquire(ctx, p.ID)
	if err != nil {
		return err
	}
	defer release()
	branches := []string{p.DefaultBranch}
	for _, b := range changes {
		branches = append(branches, b)
	}
	sort.Strings(branches[1:])
	for _, branch := range branches {
		renamed, refusal, err := renameBranch(ctx, dir, branch, by)
		switch {
		case err != nil:
			return fmt.Errorf("%s: %w", branch, err)
		case refusal != "":
			if row.Refused == nil {
				row.Refused = map[string]string{}
			}
			row.Refused[branch] = refusal
			continue
		case !renamed:
			continue
		}
		row.Renamed = append(row.Renamed, branch)
		if branch == p.DefaultBranch {
			if err := scm.RecordMain(ctx, s.db, dir, p.ID, branch, scm.ViaUpgrade, s.now()); err != nil {
				return fmt.Errorf("renamed %s, but recording where main stands failed: %w", branch, err)
			}
		}
		s.publisher.PushBranch(ctx, p.ID, dir, branch)
	}
	moved, err := moveRefs(ctx, dir)
	row.Refs = moved
	return err
}

// renameBranch commits the move of .forge/ to .apptown/ on branch, built in
// a temporary index from the branch's tip so no working tree is touched;
// the branch moves only if it still names that tip. The default branch,
// which the hub has checked out, moves by fast-forward. It answers a
// refusal rather than an error for a branch it should leave for a person.
func renameBranch(ctx context.Context, dir, branch, by string) (renamed bool, refusal string, err error) {
	tip := gitexec.HubRef(ctx, dir, "refs/heads/"+branch)
	if tip == "" {
		return false, "", nil // a change not pushed yet has no branch
	}
	listing, err := gitexec.Output(ctx, dir, "ls-tree", "-r", "--full-tree", tip, "--", ".forge", ".apptown")
	if err != nil {
		return false, "", fmt.Errorf("reading the tree: %w", err)
	}
	var old []string
	hasNew, newContract := false, ""
	for _, line := range strings.Split(listing, "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		if path := line[tab+1:]; strings.HasPrefix(path, ".forge/") {
			old = append(old, line)
		} else {
			hasNew = true
			if path == ".apptown/app.yaml" {
				newContract = line
			}
		}
	}
	gitignore, err := gitexec.OutputBytes(ctx, dir, nil, "cat-file", "blob", tip+":.gitignore")
	if err != nil {
		gitignore = nil // no .gitignore at the tip
	}
	rewritten, ignoreChanged := renameIgnoreLines(gitignore)
	// A branch already moved (an earlier run of this upgrade) still gets its
	// contract written as version 2, once.
	var contractInfo string
	if len(old) == 0 && newContract != "" {
		tab := strings.IndexByte(newContract, '\t')
		fields := strings.Fields(newContract[:tab])
		sha, err := rewriteBlob(ctx, dir, fields[2], upgradeContract)
		var refused upgradeRefusal
		if errors.As(err, &refused) {
			return false, string(refused), nil
		}
		if err != nil {
			return false, "", err
		}
		if sha != fields[2] {
			contractInfo = fmt.Sprintf("%s %s\t.apptown/app.yaml\n", fields[0], sha)
		}
	}
	switch {
	case len(old) == 0 && !ignoreChanged && contractInfo == "":
		return false, "", nil
	case len(old) > 0 && hasNew:
		return false, "has both .forge/ and .apptown/; move the files by hand in a change", nil
	}
	index, err := os.CreateTemp("", "apptown-upgrade-index-")
	if err != nil {
		return false, "", err
	}
	indexPath := index.Name()
	_ = index.Close()
	_ = os.Remove(indexPath) // git writes the index itself
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if out, err := gitexec.RunEnv(ctx, dir, env, "read-tree", tip); err != nil {
		return false, "", fmt.Errorf("reading the tip into an index: %s", out)
	}
	var info strings.Builder
	for _, line := range old {
		// "<mode> blob <sha>\t.forge/<rest>"
		tab := strings.IndexByte(line, '\t')
		fields := strings.Fields(line[:tab])
		path := line[tab+1:]
		mode, sha := fields[0], fields[2]
		if path == ".forge/app.yaml" {
			sha, err = rewriteBlob(ctx, dir, sha, upgradeContract)
			var refused upgradeRefusal
			if errors.As(err, &refused) {
				return false, string(refused), nil
			}
			if err != nil {
				return false, "", err
			}
		}
		fmt.Fprintf(&info, "0 %s\t%s\n", strings.Repeat("0", len(fields[2])), path)
		fmt.Fprintf(&info, "%s %s\t.apptown/%s\n", mode, sha, strings.TrimPrefix(path, ".forge/"))
	}
	info.WriteString(contractInfo)
	if ignoreChanged {
		sha, err := writeBlob(ctx, dir, rewritten)
		if err != nil {
			return false, "", err
		}
		fmt.Fprintf(&info, "100644 %s\t.gitignore\n", sha)
	}
	cmd := gitexec.Command(ctx, dir, gitexec.Environ(env...), "update-index", "--index-info")
	cmd.Stdin = strings.NewReader(info.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, "", fmt.Errorf("moving the files in the index: %s", out)
	}
	tree, err := outputEnv(ctx, dir, env, "write-tree")
	if err != nil {
		return false, "", fmt.Errorf("writing the tree: %w", err)
	}
	message := trailer.Message(upgradeSubject, "", trailer.Line{Key: trailer.Actor, Value: by})
	commit, err := outputEnv(ctx, dir, []string{"GIT_AUTHOR_NAME=apptown", "GIT_AUTHOR_EMAIL=apptown@localhost", "GIT_COMMITTER_NAME=apptown", "GIT_COMMITTER_EMAIL=apptown@localhost"}, "commit-tree", tree, "-p", tip, "-m", message)
	if err != nil {
		return false, "", fmt.Errorf("committing: %w", err)
	}
	head, _ := gitexec.Output(ctx, dir, "symbolic-ref", "-q", "HEAD")
	if head == "refs/heads/"+branch {
		if out, err := gitexec.Run(ctx, dir, "merge", "--ff-only", "-q", commit); err != nil {
			return false, "", fmt.Errorf("moving the checked-out branch: %s", out)
		}
		return true, "", nil
	}
	if out, err := gitexec.Run(ctx, dir, "update-ref", "-m", upgradeSubject, "refs/heads/"+branch, commit, tip); err != nil {
		return false, "", fmt.Errorf("moving the branch: %s", out)
	}
	return true, "", nil
}

// renameIgnoreLines renames the repository folder in .gitignore lines that
// name it (".forge/*", "!.forge/app.yaml", ".forge/", ".forge") and keeps
// the old folder ignored, since change folders made before the upgrade
// still hold runtime files there.
func renameIgnoreLines(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return data, false
	}
	lines := strings.Split(string(data), "\n")
	names := func(line, folder string) bool {
		bare := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(line), "!"), "/")
		return bare == folder || strings.HasPrefix(bare, folder+"/")
	}
	for _, line := range lines {
		if names(line, ".apptown") {
			return data, false // already upgraded; the kept .forge/ line stays
		}
	}
	changed := false
	for i, line := range lines {
		if names(line, ".forge") {
			lines[i] = strings.Replace(line, ".forge", ".apptown", 1)
			changed = true
		}
	}
	if !changed {
		return data, false
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out + ".forge/\n"), true
}

// upgradeContract writes a contract as version 2 (appcontract.ConvertToV2):
// memory in Mi and Gi, and any ${needs. escaped so it is still delivered as
// written. A contract that does not convert by itself is left for a person:
// the branch is refused, named in the report, and nothing on it moves.
func upgradeContract(data []byte) ([]byte, error) {
	out, _, err := appcontract.ConvertToV2(string(data))
	if err != nil {
		return nil, upgradeRefusal(".apptown/app.yaml is not upgraded: " + err.Error() + "; fix it in a change, then run the upgrade again")
	}
	return []byte(out), nil
}

// upgradeRefusal is a branch the upgrade leaves for a person, in a sentence.
type upgradeRefusal string

func (r upgradeRefusal) Error() string { return string(r) }

func rewriteBlob(ctx context.Context, dir, sha string, rewrite func([]byte) ([]byte, error)) (string, error) {
	data, err := gitexec.OutputBytes(ctx, dir, nil, "cat-file", "blob", sha)
	if err != nil {
		return "", fmt.Errorf("reading the contract: %w", err)
	}
	next, err := rewrite(data)
	if err != nil {
		return "", fmt.Errorf("rewriting the contract: %w", err)
	}
	if string(next) == string(data) {
		return sha, nil
	}
	return writeBlob(ctx, dir, next)
}

func writeBlob(ctx context.Context, dir string, data []byte) (string, error) {
	out, err := gitexec.OutputBytes(ctx, dir, strings.NewReader(string(data)), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", fmt.Errorf("writing a file: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// outputEnv runs a git that answers one object id.
func outputEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := gitexec.RunEnv(ctx, dir, env, args...)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	if !gitexec.IsObjectID(out) {
		return "", errors.New("git answered " + out)
	}
	return out, nil
}

// moveRefs moves every ref under refs/forge/ to the same name under
// refs/apptown/, each in one transaction; a name already taken under
// refs/apptown/ stops the move with both named.
func moveRefs(ctx context.Context, dir string) (int, error) {
	out, err := gitexec.Output(ctx, dir, "for-each-ref", "--format=%(objectname) %(refname)", "refs/forge/")
	if err != nil || out == "" {
		return 0, err
	}
	moved := 0
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		next := "refs/apptown/" + strings.TrimPrefix(ref, "refs/forge/")
		cmd := gitexec.Command(ctx, dir, gitexec.Environ(), "update-ref", "--stdin")
		cmd.Stdin = strings.NewReader(fmt.Sprintf("start\ncreate %s %s\ndelete %s %s\ncommit\n", next, sha, ref, sha))
		if b, err := cmd.CombinedOutput(); err != nil {
			return moved, fmt.Errorf("moving %s to %s: %s", ref, next, strings.TrimSpace(string(b)))
		}
		moved++
	}
	return moved, nil
}

// renameLibrary gives each custom guidance block and workflow whose latest
// text still names the old contract a new version with the new names,
// through the library's own doors and attributed to by. Stock content is
// renamed by stock sync when the new release starts.
func (s *Service) renameLibrary(ctx context.Context, by string) ([]string, error) {
	var renamed []string
	blocks, err := s.ways.Blocks(ctx, wow.BlockFilter{})
	if err != nil {
		return nil, err
	}
	for _, b := range blocks {
		if b.Origin != "" {
			continue
		}
		body, err := s.ways.BlockVersionBody(ctx, b.ID, b.LatestVersion)
		if err != nil {
			return renamed, fmt.Errorf("block %s: %w", b.Name, err)
		}
		checks, err := s.ways.BlockVersionChecks(ctx, b.ID, b.LatestVersion)
		if err != nil {
			return renamed, fmt.Errorf("block %s: %w", b.Name, err)
		}
		// A block's checks are part of it: their commands, evidence files
		// and rubrics name the contract as its prose does.
		next, nextChecks := RenameContractText(body), RenameContractText(checks)
		if next == body && nextChecks == checks {
			continue
		}
		if err := s.ways.BumpBlock(ctx, b.ID, next, nextChecks, by, b.Name); err != nil {
			return renamed, fmt.Errorf("block %s: %w", b.Name, err)
		}
		renamed = append(renamed, "block "+b.Name)
	}
	names, err := s.ways.CustomWorkflowNames(ctx)
	if err != nil {
		return renamed, err
	}
	entries, err := s.ways.Workflows(ctx)
	if err != nil {
		return renamed, err
	}
	custom := map[string]bool{}
	for _, n := range names {
		custom[n] = true
	}
	for _, e := range entries {
		if !custom[e.Name] {
			continue
		}
		next := RenameContractText(e.YAML)
		if next == e.YAML {
			continue
		}
		if _, err := s.ways.AmendWorkflow(ctx, e.Name, e.Version, next, by); err != nil {
			return renamed, fmt.Errorf("workflow %s: %w", e.Name, err)
		}
		renamed = append(renamed, "workflow "+e.Name)
	}
	return renamed, nil
}

var contractNames = []struct {
	old  *regexp.Regexp
	next string
}{
	// The repository folder: not a longer name (.forge-cache), not a host such as x.forge.
	{regexp.MustCompile(`(^|[^\w.])\.forge($|[^\w-])`), "${1}.apptown${2}"},
	{regexp.MustCompile(`\bFORGE_(ORIGINS|RELEASE|BROWSER_ENV\w*)\b`), "APPTOWN_${1}"},
	{regexp.MustCompile(`__FORGE_ENV__`), "__APPTOWN_ENV__"},
	{regexp.MustCompile(`(^|[^\w])_forge/`), "${1}_apptown/"},
	{regexp.MustCompile(`forge\.flokkit\.io/v1`), "apptown.dev/v1"},
}

// RenameContractText writes the AppTown contract's new names for the old
// ones in text a project or library holds: the repository folder, the
// variables and browser global AppTown gives apps, the static paths and the
// workflow format.
func RenameContractText(text string) string {
	for _, r := range contractNames {
		// Adjacent matches share a delimiter; repeat until none is left.
		for next := r.old.ReplaceAllString(text, r.next); next != text; next = r.old.ReplaceAllString(text, r.next) {
			text = next
		}
	}
	return text
}
