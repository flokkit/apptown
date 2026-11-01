package scm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/gitlab/gitlabtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func landingAttribution(candidate string) *Attribution {
	return &Attribution{
		ChangeID: idstest.N(412), ChangeNumber: 412, Title: "Add CSV export", Body: "So finance can reconcile.", OpenedBy: "bls via console/12",
		Workflow: "land@v3", RunID: idstest.N(1903), RunNumber: 1903, Candidate: candidate, LandingAuthority: "carol",
		Approvals: []AttributedApproval{{Step: "approve-landing", By: "alice", At: "2026-10-09T03:12:00Z",
			Means: "I am the human answerable for this landing.", Declared: []string{"role:project-approver"},
			AlsoApproves: []string{"It ships on my say-so."}, Candidate: candidate}},
		Overrides: []AttributedOverride{{Step: "lint", By: "bls", Reason: "flaky upstream mirror"}},
		Checks: []AttributedCheck{{Step: "tests", StepID: idstest.N(5521), Attempt: 2, Result: "passed"},
			{Step: "scan-image", StepID: idstest.N(5524), Attempt: 1, Result: "skipped"}},
	}
}

// Text AppTown did not write (a Change's title and body, a step's
// sentence, an override's reason) reaches GitLab inert: no description line
// starts with "/" (GitLab runs quick actions there, as AppTown's account);
// mentions, HTML and backtick fences stay inside code, where GitLab expands
// none of them; and because GitLab scans raw descriptions and commit
// messages for closing statements, code included, every #, ! and @ is
// separated from what follows and every link's scheme from its address, so
// no "fixes #12" or "closes <issue URL>" closes an issue. AppTown's own
// change-page link stays whole.
func TestTextFromTheRecordReachesGitLabInert(t *testing.T) {
	t.Parallel()
	a := landingAttribution("0123456789abcdef0123456789abcdef01234567")
	a.Title = "Fix ``` the `fence`"
	a.Body = "/merge\r\n/approve\n  @all see #1 and !1\n<script>alert(1)</script>\n```\n/close\nfixes #12 and closes https://gitlab.com/group/project/-/issues/13"
	a.Approvals[0].Means = "ship @all #1 !1 <b>x</b>\n/merge"
	a.Overrides[0].Reason = "a `b` c; the follow-up fixes https://gitlab.com/group/project/-/issues/12"
	page := "https://forge.example/changes/412"
	description := MergeRequestDescription(*a, page)
	for _, line := range strings.Split(description, "\n") {
		if strings.HasPrefix(line, "/") {
			t.Errorf("a description line starts a quick action: %q", line)
		}
		for _, raw := range []string{"<script>", "```"} {
			if strings.Contains(line, raw) && !strings.HasPrefix(line, "    ") && !strings.Contains(line, "`") {
				t.Errorf("%q appears outside code: %q", raw, line)
			}
		}
	}
	for _, want := range []string{
		"    /merge\n    /approve\n      @ all see # 1 and ! 1\n    <script>alert(1)</script>\n    ```\n    /close\n    fixes # 12 and closes https:// gitlab.com/group/project/-/issues/13",
		"    Fix ``` the `fence`",
		"Meaning they approved: `ship @ all # 1 ! 1 <b>x</b> /merge`",
		"overridden by `bls`: ``a `b` c; the follow-up fixes https:// gitlab.com/group/project/-/issues/12``",
		"the full change record: " + page + "\n",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("description lacks %q:\n%s", want, description)
		}
	}
	a.Approvals[0].Means = "fixes #12 for @all, see !3"
	message := mergeMessage(Source{RunID: idstest.N(1903), WorkflowName: "land", WorkflowVersion: 3, Attribution: a}, "", page)
	for _, want := range []string{"fixes # 12 for @ all, see ! 3", "the follow-up fixes https:// gitlab.com/group/project/-/issues/12", "AppTown-Change-Page: " + page + "\n"} {
		if !strings.Contains(message+"\n", want) {
			t.Errorf("merge message lacks %q:\n%s", want, message)
		}
	}
	for _, text := range []string{description, message} {
		for _, live := range []string{"#1", "#12", "!1", "!3", "@all", "https://gitlab.com"} {
			if strings.Contains(text, live) {
				t.Errorf("%q reaches GitLab live:\n%s", live, text)
			}
		}
	}
}

// An account name goes on GitLab as AppTown records it, in code so GitLab
// never links it to a GitLab user of that name; one that looks like an email
// address is withheld, in descriptions and trailers alike. AppTown's own
// actors and how a person acted read in words.
func TestNamesOnGitLabAreAccountNamesOrWithheld(t *testing.T) {
	t.Parallel()
	for receipt, want := range map[string][2]string{
		"alice":                             {"`alice`", "alice"},
		"bls via console/12":                {"`bls` (through a chat)", "bls (through a chat)"},
		"bls via mcp/claude":                {"`bls` (through a coding agent)", "bls (through a coding agent)"},
		"alice@corp.example":                {withheld, withheld},
		"alice@corp.example via git ticket": {withheld + " (through a Git push)", withheld + " (through a Git push)"},
		"apptown-system":                    {"AppTown", "AppTown"},
		"run/7":                             {"AppTown run 7", "AppTown run 7"},
	} {
		if got := publicName(receipt, true); got != want[0] {
			t.Errorf("%q in a description: %q, want %q", receipt, got, want[0])
		}
		if got := publicName(receipt, false); got != want[1] {
			t.Errorf("%q in a trailer: %q, want %q", receipt, got, want[1])
		}
	}
	a := landingAttribution("0123456789abcdef0123456789abcdef01234567")
	a.OpenedBy, a.Approvals[0].By, a.LandingAuthority, a.Overrides[0].By = "o@corp.example", "v@corp.example", "l@corp.example", "x@corp.example"
	a.Approvals[0].Declared = []string{"v@corp.example", "group:ops@corp.example", "role:project-approver"}
	text := MergeRequestDescription(*a, "") + mergeMessage(Source{Attribution: a}, "", "")
	if strings.Contains(text, "corp.example") {
		t.Fatalf("an email-shaped account name reached GitLab:\n%s", text)
	}
	if !strings.Contains(text, "Declared approvers: "+withheld+", an AppTown group (name withheld), `role:project-approver`") {
		t.Fatalf("declared approvers are not shown with names withheld:\n%s", text)
	}
}

// The approvers a step declared are shown as a declaration, never as the
// authority a vote was checked against: a step with no list reads "project
// defaults", and nothing on GitLab says someone approved as a role. The
// landing-authority fact is stated as held when the run was admitted.
func TestDeclaredApproversAreShownAsADeclaration(t *testing.T) {
	t.Parallel()
	a := landingAttribution("0123456789abcdef0123456789abcdef01234567")
	description := MergeRequestDescription(*a, "")
	if !strings.Contains(description, "Declared approvers: `role:project-approver`") || !strings.Contains(description, "who held landing authority on this change when the run was admitted") {
		t.Fatalf("description:\n%s", description)
	}
	a.Approvals[0].Declared = nil
	description = MergeRequestDescription(*a, "")
	message := mergeMessage(Source{Attribution: a}, "", "")
	if !strings.Contains(description, "Declared approvers: project defaults") {
		t.Fatalf("an empty declaration is not shown as project defaults:\n%s", description)
	}
	for _, text := range []string{description, message} {
		if strings.Contains(text, " as `role") || strings.Contains(text, "approved as") || strings.Contains(message, "role:") {
			t.Fatalf("an approval is presented as a role:\n%s", text)
		}
	}
}

// At open a merge request names who opened the change and what it is for,
// and says it waits for approval in AppTown; it shows no approvals.
func TestAnOpeningDescriptionNamesTheOpenerAndWaits(t *testing.T) {
	t.Parallel()
	a := landingAttribution("")
	a.Approvals, a.Overrides, a.Checks, a.LandingAuthority = nil, nil, nil, ""
	description := MergeRequestDescription(*a, "https://forge.example/changes/412")
	for _, want := range []string{"**AppTown change 412**, opened by `bls` (through a chat).", "    Add CSV export\n    \n    So finance can reconcile.", "Waiting for approval in AppTown. Checks, approvals and the full change record: https://forge.example/changes/412"} {
		if !strings.Contains(description, want) {
			t.Errorf("opening description lacks %q:\n%s", want, description)
		}
	}
	if strings.Contains(description, "Approvals recorded") {
		t.Fatalf("an opening description claims approvals:\n%s", description)
	}
}

func trailersOf(t *testing.T, repo, commit string) string {
	t.Helper()
	// Git's own trailer parser, as GitLab and git interpret-trailers read them.
	return gittest.Run(t, repo, "log", "-1", "--format=%(trailers:only,unfold)", commit) + "\n"
}

func landingRepository(t *testing.T) (repo, remote, base, candidate string) {
	t.Helper()
	repo = t.TempDir()
	gittest.RunAsTester(t, repo, "init", "-b", "main")
	gittest.RunAsTester(t, repo, "config", "user.name", "AppTown test")
	gittest.RunAsTester(t, repo, "config", "user.email", "forge@example.invalid")
	gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "base")
	base = gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
	gittest.RunAsTester(t, repo, "checkout", "-qb", "change")
	gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "candidate")
	candidate = gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
	gittest.RunAsTester(t, repo, "checkout", "-q", "main")
	remote = filepath.Join(t.TempDir(), "remote.git")
	gittest.RunAsTester(t, t.TempDir(), "clone", "-q", "--bare", repo, remote)
	gittest.RunAsTester(t, repo, "remote", "add", "origin", remote)
	return repo, remote, base, candidate
}

// A direct landing's merge commit names who opened the change, each
// approving vote with its sentence, the landing authority, overrides and the
// change page, as Git trailers, and inspection still finds it by its
// operation.
func TestALandingsMergeCommitNamesWhatAdmittedIt(t *testing.T) {
	t.Parallel()
	repo, _, base, candidate := landingRepository(t)
	source := Source{RunID: idstest.N(1903), ProjectID: idstest.N(1), Repository: repo, Branch: "change", DefaultBranch: "main", WorkflowName: "land", WorkflowVersion: 3,
		Candidate: candidate, Target: base, PinRequired: true, Attribution: landingAttribution(candidate)}
	driver := &gitDriver{dataDir: t.TempDir(), changeURL: func(id ids.ID) string { return "https://forge.example/changes/412" }}
	result, err := driver.Execute(context.Background(), source, Prepared{Kind: LocalMerge, Expected: base}, "land-op.1")
	if err != nil || result.Result == "" {
		t.Fatalf("merge: %+v %v", result, err)
	}
	trailers := trailersOf(t, repo, result.Result)
	for _, want := range []string{
		"AppTown-Run-Id: " + idstest.N(1903).String(), "AppTown-Workflow: land@v3", "AppTown-Change-Id: " + idstest.N(412).String(), "AppTown-Opened-By: bls (through a chat)",
		"AppTown-Approved-By: alice (step approve-landing, 2026-10-09T03:12:00Z): I am the human answerable for this landing.",
		"AppTown-Landing-Authority: carol", "AppTown-Override: lint by bls: flaky upstream mirror",
		"AppTown-Change-Page: https://forge.example/changes/412", "AppTown-Operation: land-op.1",
	} {
		if !strings.Contains(trailers, want+"\n") {
			t.Errorf("merge commit trailers lack %q:\n%s", want, trailers)
		}
	}
	if found, err := driver.mergeCommit(context.Background(), repo, "main", "land-op.1"); err != nil || found != result.Result {
		t.Fatalf("inspection lost the operation: %q %v", found, err)
	}
}

func mergeRequestDriver(t *testing.T) (*gitDriver, *gitlabtest.Server, Source) {
	t.Helper()
	repo, remote, base, candidate := landingRepository(t)
	gittest.RunAsTester(t, repo, "push", "-q", "origin", "change")
	fake := gitlabtest.New(t)
	fake.Put("71", gitlab.MergeRequest{IID: 7, State: "opened", SHA: candidate, SourceBranch: "change", TargetBranch: "main"})
	fake.OnBranch = func(_, branch string) (string, error) {
		return gittest.RunAsTester(t, remote, "rev-parse", "refs/heads/"+branch), nil
	}
	fake.OnMerge = func(m gitlabtest.Merge, mr *gitlab.MergeRequest) error {
		gittest.RunAsTester(t, remote, "update-ref", "refs/heads/main", m.SHA)
		return nil
	}
	source := Source{RunID: idstest.N(1903), ProjectID: idstest.N(1), Repository: repo, Branch: "change", DefaultBranch: "main", WorkflowName: "land", WorkflowVersion: 3,
		Candidate: candidate, Target: base, PinRequired: true, MR: MergeRequest{Project: "group/project", IID: 7}, Attribution: landingAttribution(candidate)}
	identity, provider := fixtureProviderIdentity(t, fake, source.ProjectID, remote)
	source.MR.Identity = *identity
	driver := &gitDriver{dataDir: t.TempDir(), provider: provider, changeURL: func(ids.ID) string { return "https://forge.example/changes/412" }}
	return driver, fake, source
}

// AppTown writes what admitted the merge into the merge request's
// description before it asks GitLab to merge, and the merge carries the
// same trailers.
func TestAMergeRequestIsDescribedBeforeAppTownMergesIt(t *testing.T) {
	t.Parallel()
	driver, fake, source := mergeRequestDriver(t)
	result, err := driver.Execute(context.Background(), source, Prepared{Kind: providerMerge, Expected: source.Candidate}, "observe")
	if err != nil || result.Result != source.Candidate {
		t.Fatalf("merge: %+v %v", result, err)
	}
	descriptions := fake.Descriptions("71", 7)
	if len(descriptions) != 1 || descriptions[0] != MergeRequestDescription(*source.Attribution, "https://forge.example/changes/412") {
		t.Fatalf("description written: %q", descriptions)
	}
	merges := fake.Merges()
	if len(merges) != 1 || !strings.Contains(merges[0].Message, "\nAppTown-Approved-By: alice (step approve-landing") || strings.Contains(merges[0].Message, "AppTown-Operation") {
		t.Fatalf("merge calls: %+v", merges)
	}
}

// If GitLab will not take the description, AppTown does not merge: the
// attempt fails before any merge call, saying so, and can be retried.
func TestAMergeRequestIsNotMergedWhenItsDescriptionIsRefused(t *testing.T) {
	t.Parallel()
	driver, fake, source := mergeRequestDriver(t)
	fake.RefuseDescriptions(&gitlabtest.Refusal{Status: 403, Message: "403 Forbidden"})
	_, err := driver.Execute(context.Background(), source, Prepared{Kind: providerMerge, Expected: source.Candidate}, "observe")
	if err == nil || !strings.Contains(err.Error(), "could not record the approvals on merge request !7") {
		t.Fatalf("refused description: %v", err)
	}
	if merges := fake.Merges(); len(merges) != 0 {
		t.Fatalf("merged without its description: %+v", merges)
	}
	fake.RefuseDescriptions(nil)
	if _, err := driver.Execute(context.Background(), source, Prepared{Kind: providerMerge, Expected: source.Candidate}, "observe"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// A merge request someone merged on GitLab (exactly the candidate) is
// settled, but its description is left alone: saying AppTown merged it
// would be false.
func TestAMergeRequestMergedOnGitLabIsNotDescribedAsAppTowns(t *testing.T) {
	t.Parallel()
	driver, fake, source := mergeRequestDriver(t)
	gittest.RunAsTester(t, source.Repository, "push", "-q", "origin", source.Candidate+":refs/heads/main")
	fake.Update("71", 7, func(mr *gitlab.MergeRequest) { mr.State = "merged" })
	plan, err := driver.Prepare(context.Background(), source, "")
	if err != nil || plan.Kind != existing {
		t.Fatalf("a merged request was not settled as existing: %+v %v", plan, err)
	}
	if got := fake.Descriptions("71", 7); len(got) != 0 {
		t.Fatalf("described a request AppTown did not merge: %q", got)
	}
}

// A merge's title and its merge request name the run by its number in the
// project; the trailers carry the run's UUID, chosen here apart from it.
func TestAMergeNamesTheRunByNumberAndCarriesItsID(t *testing.T) {
	t.Parallel()
	a := landingAttribution("0123456789abcdef0123456789abcdef01234567")
	a.RunID, a.RunNumber = idstest.N(1903), 40
	message := mergeMessage(Source{RunID: idstest.N(1903), RunNumber: 40, WorkflowName: "land", WorkflowVersion: 3, Attribution: a}, "", "")
	if !strings.HasPrefix(message, "apptown: complete land run 40\n") || !strings.Contains(message, "AppTown-Run-Id: "+idstest.N(1903).String()+"\n") {
		t.Fatalf("merge message:\n%s", message)
	}
	if description := MergeRequestDescription(*a, ""); !strings.Contains(description, "Workflow `land@v3`, run 40.") || strings.Contains(description, idstest.N(1903).String()) {
		t.Fatalf("description:\n%s", description)
	}
}
