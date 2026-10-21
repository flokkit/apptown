// Package changeadds reads what a change brings into its app that main does
// not have, from declarations only: the secrets secretspec.toml declares, the services and app parts
// .apptown/app.yaml declares, and for an uplift the ways of working its
// recorded adoption target brings. Nothing is inferred from source.
//
// Each row compares the commit an approval assents to with the commit
// where it meets main. A file absent on one side declares nothing there; a
// file that cannot be read or parsed makes its row unknown, never empty.
// Work words the list, digests it and records it with the vote; this
// package only reads. Packages are a later slice.
package changeadds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/strs"
)

// The rows, in the order they are shown.
const (
	Secrets  = "secrets"
	Services = "services"
	Parts    = "app parts"
	Ways     = "ways of working"
)

const contractPath = ".apptown/app.yaml"

// historicalContractPath is where the contract lived before the AppTown
// rename. A change open across the upgrade meets main at a commit from
// before it, so a side without the new path is read at the old one.
const historicalContractPath = ".forge/app.yaml"

// Comparer answers Work's AddsReader from the project's repositories.
type Comparer struct{}

var _ workflow.AddsReader = Comparer{}

// ChangeAdds reads the rows for q.Head against where it meets main. A
// commit or main it cannot find makes every repository row unknown with the
// reason; it returns an error only for a question it cannot ask.
func (Comparer) ChangeAdds(ctx context.Context, q workflow.AddsQuestion) (workflow.ChangeAdds, error) {
	if q.Head == "" || q.Hub == "" || q.DefaultBranch == "" {
		return workflow.ChangeAdds{}, fmt.Errorf("what a change adds needs its commit, its repository and its main branch")
	}
	out := workflow.ChangeAdds{Head: q.Head}
	base, repo, err := baseOf(ctx, q)
	if err != nil {
		out.Rows = unknownRows(err.Error())
	} else {
		out.Base = base
		out.Rows = Compare(side{repo, base}.file(ctx), side{repo, q.Head}.file(ctx), short(base), short(q.Head))
	}
	if q.Uplift != nil {
		out.Rows = append(out.Rows, waysRow(q.Uplift))
	}
	return out, nil
}

// File reads a committed file: found false when the commit does not hold
// it, an error when it could not be read.
type File func(path string) (body []byte, found bool, err error)

// Compare is the repository rows for two readings of the tree. baseName
// and headName name the commits in an unknown row's reason.
func Compare(base, head File, baseName, headName string) []workflow.AddsRow {
	b, h := readSide(base, baseName), readSide(head, headName)
	return []workflow.AddsRow{
		row(Secrets, b.secrets, h.secrets, b.secretsErr, h.secretsErr),
		row(Services, b.services, h.services, b.contractErr, h.contractErr),
		row(Parts, b.parts, h.parts, b.contractErr, h.contractErr),
	}
}

func unknownRows(reason string) []workflow.AddsRow {
	return []workflow.AddsRow{{Name: Secrets, Unknown: reason}, {Name: Services, Unknown: reason}, {Name: Parts, Unknown: reason}}
}

func row(name string, base, head []string, baseErr, headErr error) workflow.AddsRow {
	if err := errors.Join(baseErr, headErr); err != nil {
		return workflow.AddsRow{Name: name, Unknown: strings.ReplaceAll(err.Error(), "\n", "; ")}
	}
	return workflow.AddsRow{Name: name, Added: minus(head, base), Removed: minus(base, head)}
}

// minus is what a holds that b does not, sorted.
func minus(a, b []string) []string {
	out := []string{}
	for _, x := range a {
		if !slices.Contains(b, x) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// declared is what one commit's declarations say.
type declared struct {
	secrets, services, parts []string
	contractErr, secretsErr  error
}

func readSide(file File, name string) declared {
	var d declared
	path := contractPath
	raw, found, err := file(path)
	if err == nil && !found {
		path = historicalContractPath
		raw, found, err = file(path)
	}
	var contract *appcontract.Contract
	switch {
	case err != nil:
		d.contractErr = fmt.Errorf("reading %s at %s: %w", path, name, err)
	case found:
		if contract, err = appcontract.Parse(raw); err != nil {
			d.contractErr = fmt.Errorf("%s at %s does not parse: %v", path, name, strings.TrimPrefix(err.Error(), contractPath+": "))
		}
	}
	if d.contractErr != nil {
		// Which file declares the secrets is named in the contract.
		d.secretsErr = fmt.Errorf("the secrets file is named in %s, which could not be read at %s", path, name)
		return d
	}
	specPath := secret.SpecFile
	if contract != nil {
		d.services = slices.Clone(contract.Needs)
		d.parts = partFacts(contract)
		if contract.SecretSpecPath != "" {
			specPath = contract.SecretSpecPath
		}
		for n := range contract.SecretGenerate {
			d.secrets = append(d.secrets, n)
		}
	}
	raw, found, err = file(specPath)
	switch {
	case err != nil:
		d.secretsErr = fmt.Errorf("reading %s at %s: %w", specPath, name, err)
	case found:
		spec, err := secret.ParseSpec(raw)
		if err != nil {
			d.secretsErr = fmt.Errorf("%s at %s does not parse: %w", specPath, name, err)
			break
		}
		for _, profile := range spec.ProfileNames() {
			for _, s := range spec.SecretsIn(profile) {
				d.secrets = append(d.secrets, s.Name)
			}
		}
	}
	return d
}

// partFacts are the contract's app parts as facts a change can add or take
// away: each part, which one is public, its port, where it keeps data, and
// each init step.
func partFacts(c *appcontract.Contract) []string {
	var out []string
	for _, p := range c.Parts {
		out = append(out, "part "+p.Name)
		if p.Public {
			out = append(out, p.Name+" is the public part")
		}
		if p.Port != 0 {
			out = append(out, fmt.Sprintf("%s listens on port %d", p.Name, p.Port))
		}
		if p.DataDir != "" {
			out = append(out, fmt.Sprintf("%s keeps data in %s", p.Name, p.DataDir))
		}
	}
	for _, i := range c.Init {
		out = append(out, "init step "+i.Name)
	}
	return out
}

// waysRow reads an uplift's recorded adoption target: what it brings and
// what it retires, as Adoption recorded it when the change opened.
func waysRow(raw json.RawMessage) workflow.AddsRow {
	var c adoption.Candidate
	if err := json.Unmarshal(raw, &c); err != nil {
		return workflow.AddsRow{Name: Ways, Unknown: "the change's recorded adoption target could not be read: " + err.Error()}
	}
	added := slices.Clone(c.Added)
	added = append(added, c.Changed...)
	for _, l := range c.LanesAdded {
		added = append(added, "workflow "+l)
	}
	for _, ch := range c.NewChecks {
		added = append(added, fmt.Sprintf("check %s · %s", ch.Block, ch.Check))
	}
	removed := slices.Clone(c.Removed)
	for _, l := range c.LanesRemoved {
		removed = append(removed, "workflow "+l)
	}
	// An older version replaces a newer one: what goes is the newer one.
	for _, o := range c.Older {
		removed = append(removed, "moves back: "+o)
	}
	return workflow.AddsRow{Name: Ways, Added: minus(added, nil), Removed: minus(removed, nil)}
}

// side reads files at one commit of one repository. An empty commit is
// main before its first commit: it holds nothing.
type side struct{ repo, commit string }

func (s side) file(ctx context.Context) File {
	return func(path string) ([]byte, bool, error) {
		if s.commit == "" {
			return nil, false, nil
		}
		// ls-tree first, so a missing file is told apart from a failure,
		// and a link is refused rather than read as its target's name.
		out, err := gitexec.HubOutput(ctx, s.repo, nil, "ls-tree", "-z", "--end-of-options", s.commit, "--", path)
		if err != nil {
			return nil, false, gitError(err)
		}
		entry := strings.TrimSuffix(string(out), "\x00")
		if entry == "" {
			// Git also answers nothing when a folder on the path is a
			// link: only a path whose every folder is a real one, or
			// missing, is a file that is not there.
			return nil, false, s.parentsAreFolders(ctx, path)
		}
		meta, _, _ := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return nil, false, fmt.Errorf("unexpected entry for %s", path)
		}
		switch {
		case fields[0] == "120000":
			return nil, false, fmt.Errorf("%s is a link, which AppTown does not follow", path)
		case fields[1] != "blob":
			return nil, false, fmt.Errorf("%s is not a file", path)
		}
		body, err := gitexec.HubOutput(ctx, s.repo, nil, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, false, gitError(err)
		}
		return body, true, nil
	}
}

// parentsAreFolders refuses a path one of whose folders is a link or a
// file in the commit; a folder that is missing is no obstruction.
func (s side) parentsAreFolders(ctx context.Context, path string) error {
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		out, err := gitexec.HubOutput(ctx, s.repo, nil, "ls-tree", "-z", "--end-of-options", s.commit, "--", dir)
		if err != nil {
			return gitError(err)
		}
		entry := strings.TrimSuffix(string(out), "\x00")
		if entry == "" {
			return nil
		}
		fields := strings.Fields(strings.SplitN(entry, "\t", 2)[0])
		switch {
		case len(fields) != 3:
			return fmt.Errorf("unexpected entry for %s", dir)
		case fields[0] == "120000":
			return fmt.Errorf("%s is a link, which AppTown does not follow", dir)
		case fields[1] != "tree":
			return fmt.Errorf("%s is not a folder", dir)
		}
	}
	return nil
}

// baseOf is where q.Head meets main, and a repository that holds both.
// Main with no commit yet is an empty base.
func baseOf(ctx context.Context, q workflow.AddsQuestion) (base, repo string, err error) {
	main, err := gitexec.HubReadRef(ctx, q.Hub, "refs/heads/"+q.DefaultBranch)
	if err != nil {
		return "", "", fmt.Errorf("main could not be read: %v", gitError(err))
	}
	for _, r := range q.Repositories {
		if r == "" || !holds(ctx, r, q.Head) {
			continue
		}
		if main == "" {
			return "", r, nil
		}
		if !holds(ctx, r, main) {
			continue
		}
		out, err := gitexec.HubOutput(ctx, r, nil, "merge-base", "--end-of-options", q.Head, main)
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 && len(strings.TrimSpace(string(out))) == 0 {
				return "", "", fmt.Errorf("commit %s shares no history with main", short(q.Head))
			}
			return "", "", fmt.Errorf("where commit %s meets main could not be read: %v", short(q.Head), gitError(err))
		}
		return strings.TrimSpace(string(out)), r, nil
	}
	return "", "", fmt.Errorf("commit %s and main are not both in the change's folder or the project's repository", short(q.Head))
}

func holds(ctx context.Context, repo, commit string) bool {
	sha, err := gitexec.HubReadRef(ctx, repo, commit+"^{commit}")
	return err == nil && sha != ""
}

// gitError carries what Git said on stderr, which an exit status alone
// does not.
func gitError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return fmt.Errorf("%s", strings.TrimSpace(string(exit.Stderr)))
	}
	return err
}

// short names a commit; an empty base is main's start, not a missing commit.
func short(sha string) string {
	if sha == "" {
		return "main's start"
	}
	return strs.ShortSHA(sha)
}
