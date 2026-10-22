package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"golang.org/x/term"
)

// resolveProject: explicit flag > APPTOWN_PROJECT > cwd basename when it names a
// real project. The goal: inside a checkout, lane verbs need zero flags.
func resolveProject(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if v := os.Getenv("APPTOWN_PROJECT"); v != "" {
		return v, nil
	}
	cwd, err := os.Getwd()
	if err == nil {
		base := filepath.Base(cwd)
		var ps []struct{ Slug string }
		// A lookup AppTown refused or could not answer is that error, not
		// a command line without a project.
		if err := api.get("/projects", &ps); err != nil {
			return "", fmt.Errorf("finding the project named after this directory: %w", err)
		}
		for _, p := range ps {
			if p.Slug == base {
				return base, nil
			}
		}
	}
	return "", usagef("no project: pass -p <slug>, set APPTOWN_PROJECT, or run from a directory named after the project")
}

func cmdProjects() error {
	var ps []struct {
		Slug, Name  string
		ActiveItems int
		AdrCount    int
	}
	if err := api.get("/projects", &ps); err != nil {
		return err
	}
	w := tw()
	fmt.Fprintln(w, "SLUG\tNAME\tACTIVE\tADRS")
	for _, p := range ps {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", p.Slug, p.Name, p.ActiveItems, p.AdrCount)
	}
	return w.Flush()
}

// cmdWorkflows lists a project's workflows with their plain reading, or —
// given a name — prints one workflow's full story: stages, steps, gates.
// The summary comes from the server (the engine's own parser), so the CLI
// and the web read the same words.
func cmdWorkflows(args []string) error {
	fs := newFlags("workflows")
	project := fs.String("p", "", "project slug")
	pos := append(parseArgs(fs, args), "")
	if *project == "" {
		return usagef("-p <slug> is required")
	}
	type summary struct {
		Description string `json:"description"`
		Pacing      string `json:"pacing"`
		Trigger     string `json:"trigger"`
		Flow        string `json:"flow"`
		Stages      []struct {
			Name     string `json:"name"`
			GateText string `json:"gateText"`
			Steps    []struct {
				Name  string `json:"name"`
				Icon  string `json:"icon"`
				Gloss string `json:"gloss"`
				Human bool   `json:"human"`
			} `json:"steps"`
		} `json:"stages"`
	}
	var wfs []struct {
		Name, Kind, Origin, LastRunAt string
		Version, RunsCount            int
		Modified                      bool
		Summary                       *summary
	}
	if err := api.get("/projects/"+*project+"/workflows", &wfs); err != nil {
		return err
	}
	if name := pos[0]; name != "" {
		for _, wf := range wfs {
			if wf.Name != name {
				continue
			}
			fmt.Printf("%s  v%d", bold(wf.Name), wf.Version)
			if wf.Origin != "" {
				fmt.Printf("  (from %s%s)", wf.Origin, map[bool]string{true: ", modified here"}[wf.Modified])
			}
			fmt.Println()
			if s := wf.Summary; s != nil {
				fmt.Printf("%s", s.Pacing)
				if s.Trigger != "" {
					fmt.Printf(" · %s", s.Trigger)
				}
				if wf.RunsCount > 0 {
					fmt.Printf(" · used by %d runs", wf.RunsCount)
				}
				fmt.Println()
				if s.Description != "" {
					fmt.Println(s.Description)
				}
				for i, st := range s.Stages {
					fmt.Printf("\n%d. %s\n", i+1, bold(st.Name))
					for _, step := range st.Steps {
						fmt.Printf("   %s %-22s %s\n", step.Icon, step.Name, step.Gloss)
					}
					fmt.Printf("   → %s\n", st.GateText)
				}
			} else {
				fmt.Println("(this workflow's YAML does not parse, so its steps cannot be listed)")
			}
			return nil
		}
		return fmt.Errorf("no workflow %q on %s", name, *project)
	}
	w := tw()
	fmt.Fprintln(w, "WORKFLOW\tPACING\tUSED\tWHAT HAPPENS")
	for _, wf := range wfs {
		pacing, flow := "?", ""
		if wf.Summary != nil {
			pacing, flow = wf.Summary.Pacing, wf.Summary.Flow
		}
		used := "never"
		if wf.RunsCount > 0 {
			used = fmt.Sprintf("%d×", wf.RunsCount)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", wf.Name, pacing, used, flow)
	}
	w.Flush()
	fmt.Println("\n(apptown workflows -p <proj> <name> shows one workflow's stages and steps)")
	return nil
}

func cmdLog(args []string) error {
	fs := newFlags("log")
	project := fs.String("p", "", "project slug")
	kind := fs.String("k", "", "kind prefix filter (e.g. adr, workflow, guidance)")
	n := fs.Int("n", 30, "max entries")
	parseArgs(fs, args)
	q := fmt.Sprintf("?limit=%d", *n)
	if *project != "" {
		q += "&project=" + *project
	}
	if *kind != "" {
		q += "&kind=" + *kind
	}
	var entries []struct {
		At          string
		Actor       string
		ActorShown  string
		ProjectSlug string
		Kind        string
		Subject     string
	}
	if err := api.get("/audit"+q, &entries); err != nil {
		return err
	}
	w := tw()
	fmt.Fprintln(w, "AT\tACTOR\tPROJECT\tKIND\tSUBJECT")
	for _, e := range entries {
		// The actor as the person is now (renamed, removed), when AppTown says.
		actor := e.ActorShown
		if actor == "" {
			actor = e.Actor
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			strings.Replace(strings.TrimSuffix(e.At, "Z"), "T", " ", 1), actor, e.ProjectSlug, e.Kind, e.Subject)
	}
	return w.Flush()
}

// Setup verbs: the one-command project bootstrap the wizard does in the web
// UI. `apptown new` composes the same four endpoints — create, repo init,
// remote attach, pack apply — plus the stock toolchain declaration.

// importFailed says what a failed repository import left. Only the
// forge's own refusal (a 4xx answer) shows the import did not happen, so
// only then is the project said to have no repository, to delete and try
// again. Any other failure after the request was sent (a lost answer, a
// broken connection, a 5xx) may have started it, so nothing destructive is
// advised.
func importFailed(from, slug string, err error) error {
	var refused *httpError
	if errors.As(err, &refused) && refused.final() {
		return fmt.Errorf("start from %s: %w (the project exists without a repository; `apptown delete %s` and try again)", from, err, slug)
	}
	return fmt.Errorf("start from %s: %w (the project %s exists; whether its repository was started is unknown, so check before deleting anything)", from, err, slug)
}

func cmdNew(args []string) error {
	fs := newFlags("new")
	name := fs.String("name", "", "display name (default: slug)")
	ns := fs.String("ns", "", "namespace (display grouping on the Projects page)")
	pack := fs.String("pack", "", "pack to apply (e.g. java-spring, go-small)")
	toolchain := fs.String("toolchain", "", "stock toolchain to declare in mise.toml (java, go, node, python, rust)")
	remote := fs.Bool("remote", false, "create + push a GitLab remote in the first available namespace")
	branch := fs.String("branch", "", "default branch (a new repository: main; --from: the source's own default)")
	from := fs.String("from", "", "start from existing code: an https Git URL or a Git bundle file (its default branch and tags; nothing else)")
	gitSecret := fs.String("secret-for-git", "", "with an https --from: an AppTown secret to offer as the git password (administrators only)")
	asJSON := fs.Bool("json", false, "print the project as JSON; what is said along the way goes to stderr")
	args = parseArgs(fs, args)
	if len(args) < 1 || args[0] == "" {
		return usagef("usage: apptown new <slug> [--from <https-url|bundle-file> [--secret-for-git NAME] [--branch B]] [--name N] [--pack P] [--toolchain T] [--remote]")
	}
	slug := args[0]
	if *gitSecret != "" && !strings.HasPrefix(*from, "https://") {
		return usagef("--secret-for-git goes with an https --from URL")
	}
	var bundle *os.File
	if *from != "" && !strings.HasPrefix(*from, "https://") {
		// Opened before the project is made: a mistyped path should not
		// leave an empty project behind.
		f, err := os.Open(*from)
		if err != nil {
			return usagef("--from is neither an https URL nor a readable bundle file: %v", err)
		}
		defer f.Close()
		bundle = f
	}

	if *asJSON {
		defer stdoutToStderr()()
	}
	var proj struct {
		ID         ids.ID
		Slug, Name string
	}
	// The pack goes with the creation so a pack AppTown does not offer
	// refuses it before any project is made.
	if err := api.post("/projects", map[string]any{"slug": slug, "name": *name, "namespace": *ns, "pack": *pack}, &proj); err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	fmt.Printf("project %s created\n", proj.Slug)

	var repo struct{ DefaultBranch string }
	switch {
	case bundle != nil:
		raw, err := api.upload("/projects/"+slug+"/repo/from-bundle?branch="+url.QueryEscape(*branch), bundle)
		if err == nil {
			err = json.Unmarshal(raw, &repo)
		}
		if err != nil {
			return importFailed(*from, slug, err)
		}
		fmt.Printf("repo started from %s on %s\n", *from, repo.DefaultBranch)
	case *from != "":
		if err := api.post("/projects/"+slug+"/repo/from-url", map[string]any{"url": *from, "branch": *branch, "credentialSecret": *gitSecret}, &repo); err != nil {
			return importFailed(*from, slug, err)
		}
		fmt.Printf("repo started from %s on %s\n", *from, repo.DefaultBranch)
	default:
		b := *branch
		if b == "" {
			b = "main"
		}
		if err := api.post("/projects/"+slug+"/repo", map[string]any{"defaultBranch": b}, &repo); err != nil {
			return fmt.Errorf("init repo: %w", err)
		}
		fmt.Printf("repo initialized on %s\n", repo.DefaultBranch)
	}

	if *toolchain != "" {
		var tc struct{ Toolchain string }
		if err := api.post("/projects/"+slug+"/toolchain", map[string]any{"name": *toolchain}, &tc); err != nil {
			return fmt.Errorf("toolchain: %w", err)
		}
		fmt.Printf("toolchain %s declared in mise.toml\n", tc.Toolchain)
	}

	if *remote {
		var ns struct {
			Namespaces []struct {
				ID   int    `json:"id"`
				Path string `json:"path"`
			}
		}
		if err := api.get("/scm/namespaces", &ns); err != nil || len(ns.Namespaces) == 0 {
			return fmt.Errorf("no SCM namespace available: %w", err)
		}
		var rr struct{ Remote, Pushed string }
		if err := api.post("/projects/"+slug+"/repo/remote",
			map[string]any{"create": true, "namespaceId": ns.Namespaces[0].ID}, &rr); err != nil {
			return fmt.Errorf("remote: %w", err)
		}
		fmt.Printf("remote %s (pushed %s)\n", rr.Remote, rr.Pushed)
	}

	if *pack != "" {
		if err := applyPack(slug, *pack); err != nil {
			return err
		}
	}
	if *from != "" {
		printDeclaredSecrets(slug)
	}
	var setup struct {
		NeedsModel bool `json:"needsModel"`
	}
	// Unread, the hint is today's: the model work most AppTowns can do.
	_ = api.get("/setup", &setup)
	fmt.Printf("\n%s\n", firstChangeHint(slug, setup.NeedsModel))
	if *asJSON {
		return printJSON(ProjectCreated{ID: proj.ID, Slug: proj.Slug, Name: proj.Name})
	}
	return nil
}

// printDeclaredSecrets names the secrets the code declares (secretspec.toml),
// which a project started from existing code still needs set here.
func printDeclaredSecrets(slug string) {
	var out struct {
		Declared bool
		Problem  string
		Secrets  []struct {
			Name     string
			Generate bool
		}
	}
	if err := api.get("/projects/"+slug+"/secrets", &out); err != nil || !out.Declared {
		return
	}
	var set, minted []string
	for _, s := range out.Secrets {
		if s.Generate {
			minted = append(minted, s.Name)
		} else {
			set = append(set, s.Name)
		}
	}
	if len(set) > 0 {
		fmt.Printf("the code declares secrets to set (Settings → Secrets): %s\n", strings.Join(set, ", "))
	}
	if len(minted) > 0 {
		fmt.Printf("generated by AppTown on first deploy: %s\n", strings.Join(minted, ", "))
	}
}

// cmdBundle writes a project's code (its default branch and tags) as a Git
// bundle to standard output: the way to move a project to another AppTown
// (apptown new <slug> --from p.bundle there).
func cmdBundle(args []string) error {
	fs := newFlags("bundle")
	project := fs.String("p", "", "project slug")
	parseArgs(fs, args)
	if *project == "" {
		return usagef("usage: apptown bundle -p <slug> > <slug>.bundle")
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return usagef("the bundle is binary: send it to a file, apptown bundle -p %s > %s.bundle", *project, *project)
	}
	n, err := api.download("/projects/"+*project+"/repo/bundle", os.Stdout)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s: %d bytes (its default branch and tags); start it elsewhere with: apptown new %s --from <file>\n", *project, n, *project)
	return nil
}

// cmdDelete deletes a project after the person types its slug: it stops
// taking work at once, its deployments are taken down one at a time (main
// last) after a ten-minute grace, then it is kept, read-only, until it is
// purged (design/subsystems/project-deletion.md). With --cancel it cancels
// a deletion before its first take-down; restore and purge are the other
// acts on a deleted project.
func cmdDelete(args []string) error {
	fs := newFlags("delete")
	cancel := fs.Bool("cancel", false, "cancel the deletion (only before its first take-down)")
	noWait := fs.Bool("no-wait", false, "ask for the deletion and print where it stands, without following it")
	pos := parseArgs(fs, args)
	if len(pos) != 1 || pos[0] == "" {
		return usagef("usage: apptown delete <project-slug> [--cancel] [--no-wait]")
	}
	slug := pos[0]
	if *cancel {
		var st deletionStatus
		raw, err := api.request("POST", "/projects/"+slug+"/deletion/cancel", map[string]string{})
		if err != nil {
			return err
		}
		_ = json.Unmarshal(raw, &st)
		fmt.Printf("%s the deletion of %s is cancelled; it is %s\n", green("✓"), slug, st.State)
		return nil
	}
	var deps []struct{ Name string }
	if err := api.get("/projects/"+slug+"/deployments", &deps); err != nil {
		return err
	}
	fmt.Printf("Deleting %s:\n", bold(slug))
	fmt.Println("  - it takes no new work at once; for ten minutes the deletion can be cancelled (apptown delete --cancel)")
	for _, d := range deps {
		if d.Name != "" {
			fmt.Printf("  - its %s deployment is taken down, with a final copy of its data (main last)\n", d.Name)
		}
	}
	fmt.Println("  - then it is kept, read-only, with its backups, for 30 days (apptown restore brings it back until its purge starts)")
	fmt.Println("  - then it is purged: its backups, repository, changes, runs and releases are removed; its GitLab copy is archived")
	fmt.Println(dim("  The usage ledger and receipts are kept. Work still running must finish first."))
	if ans := ask(fmt.Sprintf("type %q to delete it: ", slug)); ans != slug {
		return fmt.Errorf("nothing deleted: what was typed is not %q", slug)
	}
	var st deletionStatus
	raw, err := api.request("DELETE", "/projects/"+slug, map[string]string{"confirm": slug})
	if err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &st)
	fmt.Printf("%s %s is being deleted %s\n", yellow("⏹"), slug, dim("(Ctrl-C stops watching; the deletion carries on)"))
	if *noWait {
		return nil
	}
	return followDeletion(slug)
}

// deletionStatus is where a project's deletion stands, as AppTown says.
type deletionStatus struct {
	State          string   `json:"state"`
	Stalled        string   `json:"stalled"`
	Remaining      []string `json:"remaining"`
	KeptUntil      string   `json:"keptUntil"`
	BindingRevoked bool     `json:"bindingRevoked"`
}

// followDeletion reports a deletion until the project is deleted (kept),
// or the deletion stops for a person.
func followDeletion(slug string) error {
	said := ""
	for {
		var st deletionStatus
		if err := api.get("/projects/"+slug+"/deletion", &st); err != nil {
			return fmt.Errorf("the deletion's progress could not be read: %w", err)
		}
		switch {
		case st.State == "deleted":
			fmt.Printf("%s %s is deleted, kept until %s; apptown restore %s brings it back until its purge starts\n", green("✓"), slug, st.KeptUntil, slug)
			return nil
		case st.State == "active":
			return fmt.Errorf("%s is active: its deletion was cancelled", slug)
		case st.Stalled != "":
			return fmt.Errorf("the deletion of %s stopped: %s — run apptown delete %s again to go on", slug, st.Stalled, slug)
		}
		now := "waiting on: " + strings.Join(st.Remaining, ", ")
		if len(st.Remaining) == 0 {
			now = "nothing left to take down"
		}
		if now != said {
			said = now
			fmt.Printf("  … %s\n", now)
		}
		pause(2 * pollEvery)
	}
}

// cmdRestore brings a deleted project back to active, with no deployments,
// until its purge starts.
func cmdRestore(args []string) error {
	pos := parseArgs(newFlags("restore"), args)
	if len(pos) != 1 || pos[0] == "" {
		return usagef("usage: apptown restore <project-slug>")
	}
	if _, err := api.request("POST", "/projects/"+pos[0]+"/restore", map[string]string{}); err != nil {
		return err
	}
	fmt.Printf("%s %s is active again, with no deployments; deploy it from a final copy on its Backups page\n", green("✓"), pos[0])
	return nil
}

// cmdPurge purges a deleted project now (administrators), and follows the
// purge to its end.
func cmdPurge(args []string) error {
	fs := newFlags("purge")
	noWait := fs.Bool("no-wait", false, "print the purge's link without following it")
	pos := parseArgs(fs, args)
	if len(pos) != 1 || pos[0] == "" {
		return usagef("usage: apptown purge <project-slug> [--no-wait]")
	}
	slug := pos[0]
	if ans := ask(fmt.Sprintf("purging removes %s's backups, repository and records for good; type %q to purge it: ", slug, slug)); ans != slug {
		return fmt.Errorf("nothing purged: what was typed is not %q", slug)
	}
	var admitted struct {
		OperationID ids.ID `json:"operationId"`
	}
	raw, err := api.request("POST", "/projects/"+slug+"/purge", map[string]string{})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &admitted); err != nil || admitted.OperationID.IsZero() {
		return fmt.Errorf("AppTown's answer named no purge: %s", raw)
	}
	fmt.Printf("purge of %s: operation %d\n", slug, admitted.OperationID)
	if *noWait {
		return nil
	}
	return followOperation(os.Stdout, admitted.OperationID, pollEvery)
}

func cmdPack(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "versions":
			return cmdPackVersions(args[1:])
		case "recommend":
			return cmdPackRecommend(args[1:])
		}
	}
	fs := newFlags("pack")
	slug := fs.String("p", "", "project slug")
	pos := parseArgs(fs, args)
	if *slug == "" || len(pos) != 1 {
		return usagef("usage: apptown pack -p <slug> <pack-name>")
	}
	return applyPack(*slug, pos[0])
}

func applyPack(slug, pack string) error {
	var res struct {
		Pack               string
		WorkflowsInstalled []string
		PoliciesInstalled  []string
		GuidanceImports    int
		Missing            []string
		Skipped            []string
		PolicyWarnings     []string
	}
	if err := api.post("/projects/"+slug+"/pack", map[string]any{"name": pack}, &res); err != nil {
		return fmt.Errorf("apply pack: %w", err)
	}
	fmt.Printf("pack %s applied: its ways of working are %d workflows, %d policies and %d guidance documents\n",
		res.Pack, len(res.WorkflowsInstalled), len(res.PoliciesInstalled), res.GuidanceImports)
	for _, m := range res.Missing {
		fmt.Println("  warning: missing", m)
	}
	for _, f := range res.Skipped {
		fmt.Println("  not written:", f, "(the project already has changes; add it through a change)")
	}
	for _, w := range res.PolicyWarnings {
		fmt.Println("  policy warning:", w)
	}
	return nil
}

// cmdInvite asks AppTown for a one-time invite link (admin) and prints it.
// AppTown sends no email: the administrator passes the link on.
func cmdInvite(args []string) error {
	fs := newFlags("invite")
	role := fs.String("role", "member", "member | admin")
	label := fs.String("label", "", "a display name or email, kept as a label only")
	days := fs.Int("days", 0, "how many days the link lasts (default 7, at most 30)")
	pos := parseArgs(fs, args)
	if len(pos) != 1 || pos[0] == "" {
		return usagef("usage: apptown invite <username> [--role member|admin] [--label text] [--days N]")
	}
	var res struct {
		Link   string
		Note   string
		Invite struct{ Username, Role, ExpiresAt string }
	}
	if err := api.post("/invites", map[string]any{"username": pos[0], "role": *role, "label": *label, "days": *days}, &res); err != nil {
		return err
	}
	fmt.Printf("%s\n  user %s · role %s · expires %.16s\n  %s\n", res.Link, res.Invite.Username, res.Invite.Role, res.Invite.ExpiresAt, res.Note)
	return nil
}

// cmdPasswordLink asks AppTown for a one-time password reset link (admin)
// and prints it. Like an invite, the administrator hands the link over; the
// account's password and sessions end only when the link is used.
func cmdPasswordLink(args []string) error {
	fs := newFlags("password-link")
	days := fs.Int("days", 0, "how many days the link lasts (default 3, at most 30)")
	pos := parseArgs(fs, args)
	if len(pos) != 1 || pos[0] == "" {
		return usagef("usage: apptown password-link <username> [--days N]")
	}
	var res struct {
		Link   string
		Note   string
		Invite struct{ Username, ExpiresAt string }
	}
	if err := api.post("/password-links", map[string]any{"username": pos[0], "days": *days}, &res); err != nil {
		return err
	}
	fmt.Printf("%s\n  password reset for %s · expires %.16s\n  %s\n", res.Link, res.Invite.Username, res.Invite.ExpiresAt, res.Note)
	return nil
}

func cmdToken(args []string) error {
	if len(args) < 1 {
		return usagef("usage: apptown token create|list|revoke")
	}
	switch args[0] {
	case "create":
		fs := newFlags("token create")
		u := fs.String("u", "", "token acts as this user (default: you)")
		role := fs.String("r", "", "member | admin (default: a cli token gets its person's own role, an agent token member)")
		purpose := fs.String("purpose", "", "cli (your own command line; acts as you) | agent (a coding agent's MCP connection; may not approve, land or administer)")
		pos := parseArgs(fs, args[1:])
		if len(pos) < 1 || pos[0] == "" || *purpose == "" {
			return usagef("usage: apptown token create <name> --purpose cli|agent [-u user] [-r role]")
		}
		var res struct{ Token, Name, User, Role, Purpose, Note string }
		if err := api.post("/tokens", map[string]any{"name": pos[0], "user": *u, "role": *role, "purpose": *purpose}, &res); err != nil {
			return err
		}
		fmt.Printf("%s\n  name %s · user %s · role %s · purpose %s\n  %s\n", res.Token, res.Name, res.User, res.Role, res.Purpose, res.Note)
		return nil
	case "list":
		var res struct {
			Tokens []struct{ Name, User, Role, Purpose, CreatedAt, LastUsed, RevokedAt string }
		}
		if err := api.get("/tokens", &res); err != nil {
			return err
		}
		w := tw()
		fmt.Fprintln(w, "NAME\tUSER\tROLE\tPURPOSE\tCREATED\tLAST USED\tREVOKED")
		for _, t := range res.Tokens {
			purpose := t.Purpose
			if purpose == "" {
				purpose = "none (refused)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.10s\t%.10s\t%.10s\n", t.Name, t.User, t.Role, purpose, t.CreatedAt, t.LastUsed, t.RevokedAt)
		}
		return w.Flush()
	case "revoke":
		if len(args) < 2 {
			return usagef("usage: apptown token revoke <name>")
		}
		if _, err := api.request("DELETE", "/tokens/"+args[1], nil); err != nil {
			return err
		}
		fmt.Println("revoked", args[1])
		return nil
	}
	return usagef("unknown token subcommand %q", args[0])
}

// packHistory is the pack page's read: GET /packs/{name}.
type packHistory struct {
	Name          string
	Recommended   int64
	Latest        int64
	FollowRefused string
	Versions      []struct {
		Version         int64
		Name            string
		BasedOn         int64
		IncludedPack    string
		IncludedVersion int64
		Note            string
	}
}

func cmdPackVersions(args []string) error {
	if len(args) != 1 {
		return usagef("usage: apptown pack versions <pack>")
	}
	var h packHistory
	if err := api.get("/packs/"+args[0], &h); err != nil {
		return err
	}
	printPackHistory(h)
	return nil
}

func printPackHistory(h packHistory) {
	fmt.Printf("%s: v%d recommended (new projects get it; projects on others are offered it), v%d newest\n", h.Name, h.Recommended, h.Latest)
	for _, v := range h.Versions {
		mark := "  "
		if v.Version == h.Recommended {
			mark = "* "
		}
		line := fmt.Sprintf("%sv%d", mark, v.Version)
		if v.Name != "" {
			line += " " + strconv.Quote(v.Name)
		}
		if v.BasedOn > 0 {
			line += fmt.Sprintf(" from v%d", v.BasedOn)
		}
		if v.IncludedPack != "" {
			line += fmt.Sprintf(", includes %s v%d", v.IncludedPack, v.IncludedVersion)
		}
		fmt.Println(line)
		if v.Note != "" && v.Note != "published contents" {
			fmt.Println("      " + v.Note)
		}
	}
	if h.FollowRefused != "" {
		fmt.Println("the last follow was not made: " + h.FollowRefused)
	}
}

// cmdPackRecommend moves a pack's recommendation; --what-if names the
// what-if the person decided on, which AppTown checks is a finished one
// of that version and records on the recommendation.
func cmdPackRecommend(args []string) error {
	fs := newFlags("pack recommend")
	whatIf := fs.String("what-if", "", "the what-if of this version the recommendation was decided on")
	pos := parseArgs(fs, args)
	if len(pos) != 2 {
		return usagef("usage: apptown pack recommend <pack> <version> [--what-if <id>]")
	}
	version, err := strconv.ParseInt(strings.TrimPrefix(pos[1], "v"), 10, 64)
	if err != nil || version < 1 {
		return usagef("version must be a number such as 3 or v3, not %q", pos[1])
	}
	body := map[string]any{"version": version}
	basis := ""
	if *whatIf != "" {
		id, err := parseID(*whatIf, "--what-if")
		if err != nil {
			return err
		}
		body["whatIf"], basis = id, fmt.Sprintf(", as assessed in what-if %s", id)
	}
	var h packHistory
	if err := api.post("/packs/"+pos[0]+"/recommended", body, &h); err != nil {
		return fmt.Errorf("recommend: %w", err)
	}
	fmt.Printf("%s v%d is recommended%s. No project moved: each is offered it on its Settings page.\n", h.Name, version, basis)
	printPackHistory(h)
	return nil
}

// cmdSchedules lists what a project's schedules will do and last did, in
// AppTown's time (UTC), the time their crons are read in.
func cmdSchedules(args []string) error {
	fs := newFlags("schedules")
	project := fs.String("p", "", "project slug")
	parseArgs(fs, args)
	if *project == "" {
		return usagef("-p <slug> is required")
	}
	var list struct {
		EvalCopy  bool `json:"evalCopy"`
		Schedules []struct {
			Source struct {
				Workflow string `json:"workflow"`
				Version  int    `json:"version"`
			} `json:"source"`
			Cron    string `json:"cron"`
			Next    string `json:"next"`
			Due     bool   `json:"due"`
			Problem string `json:"problem"`
			Last    *struct {
				At       string `json:"at"`
				Outcome  string `json:"outcome"`
				ChangeID ids.ID `json:"changeId"`
			} `json:"last"`
		} `json:"schedules"`
	}
	if err := api.get("/projects/"+*project+"/schedules", &list); err != nil {
		return err
	}
	if list.EvalCopy {
		fmt.Println("an eval copy: it runs no schedules")
		return nil
	}
	if len(list.Schedules) == 0 {
		fmt.Println("no schedules: none of this project's workflows declares one")
		return nil
	}
	utc := func(s string) string {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return "unknown"
		}
		return t.UTC().Format("2006-01-02 15:04")
	}
	w := tw()
	fmt.Fprintln(w, "WORKFLOW\tCRON (UTC)\tNEXT (UTC)\tLAST (UTC)")
	for _, s := range list.Schedules {
		next, last := utc(s.Next), "never"
		switch {
		case s.Problem != "":
			next, last = "unknown", "unknown: "+s.Problem
		case s.Due:
			next = "due now"
		}
		if s.Last != nil && s.Problem == "" {
			switch s.Last.Outcome {
			case "opened":
				last = fmt.Sprintf("%s opened change %s", utc(s.Last.At), s.Last.ChangeID)
			case "skipped":
				last = utc(s.Last.At) + " skipped: previous run still active"
			case "failed":
				last = utc(s.Last.At) + " could not start"
			default:
				last = utc(s.Last.At) + " outcome unknown"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Source.Workflow, s.Cron, next, last)
	}
	return w.Flush()
}

// firstChangeHint is the next step after apptown new that works on this
// AppTown: asking for a change needs a model, and with none set up a
// person makes the first change and lands it.
func firstChangeHint(slug string, needsModel bool) string {
	if needsModel {
		return fmt.Sprintf("%[1]s is ready. No AI model is set up on this AppTown yet, so make the first change yourself: apptown start -p %[1]s -t \"<title>\", push your work to its branch, then land it with apptown land.\n"+
			"(Once an administrator adds an Anthropic or OpenRouter API key on the Home page, ask for changes: apptown quick -p %[1]s \"<what to change>\")", slug)
	}
	return fmt.Sprintf("%[1]s is ready. ask for a first change: apptown quick -p %[1]s \"<what to change>\"", slug)
}
