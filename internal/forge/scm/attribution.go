package scm

import (
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/strs"
	"flokkit.io/forge/internal/trailer"
)

// Attribution is what the forge's record says about a change where GitLab
// readers see it: who opened it and what it is for and, for a landing, what
// admitted the merge. Workflow reads it from its own records; at a landing it
// reads it in the transaction that admits the merge and again at each
// check-in, so it names what admitted this merge, never an earlier reading.
// SCM renders it into the merge request's description and the merge commit's
// trailers. It is plain data; nothing here is an input to a gate.
//
// Names are receipt names as the record holds them ("alice", "alice via
// console/12", "run/7", "apptown-system"); rendering decides what a public page
// shows (publicName).
type Attribution struct {
	ChangeID     ids.ID
	ChangeNumber int64 // within its project: how people name it
	Title, Body  string
	OpenedBy     string
	// Workflow is the run's definition as name@vN.
	Workflow  string
	RunID     ids.ID
	RunNumber int64 // within its project, as people name it
	// Candidate is the pinned commit a landing merges; empty at open.
	Candidate string
	// LandingAuthority: who started the run while holding landing authority
	// on its change, as recorded when the run was admitted. It does not say
	// they hold it now.
	LandingAuthority string
	// EvalAuthority names the eval whose authority admitted the run in one
	// of its copies, when that is the fact.
	EvalAuthority string
	Approvals     []AttributedApproval
	Overrides     []AttributedOverride
	Checks        []AttributedCheck
}

// AttributedApproval is one approving vote of a deciding round, or an
// approval an eval settled (Eval set, no person).
type AttributedApproval struct {
	Step string
	By   string
	At   string
	// Means is the sentence the step asked the person to agree to, as the
	// vote's round recorded it.
	Means string
	// Declared is who the workflow said may approve. It is a declaration,
	// not the authority the vote was checked against: an instance root
	// passes without matching it, and an empty list means the project's
	// default approvers at the time of the vote.
	Declared []string
	// AlsoApproves are the landing sentences an acceptance agreed to.
	AlsoApproves []string
	Candidate    string
	Eval         string
}

// AttributedOverride is a step a person overrode, with their reason.
type AttributedOverride struct{ Step, By, Reason string }

// AttributedCheck is a check on the landing's candidate and the result its
// step recorded, in words: "passed", "skipped", "overridden", ...
type AttributedCheck struct {
	Step    string
	StepID  ids.ID
	Attempt int64
	Result  string
}

// MergeRequestDescription is the description the forge writes on a merge
// request it opened: at open, who opened the change and what it is for; at a
// landing (Candidate set), also the approvals, overrides and checks that
// admitted it. The forge owns the whole description and writes it whole.
// Text the forge did not write reaches GitLab only as code (a code span or an
// indented code block), where GitLab expands no references or mentions, and
// no line the forge writes starts with "/", so no quick action runs.
func MergeRequestDescription(a Attribution, changeURL string) string {
	var b strings.Builder
	b.WriteString("**AppTown change")
	if a.ChangeNumber > 0 {
		fmt.Fprintf(&b, " %d", a.ChangeNumber)
	}
	b.WriteString("**")
	if a.OpenedBy != "" {
		b.WriteString(", opened by " + publicName(a.OpenedBy, true))
	}
	fmt.Fprintf(&b, ". Workflow %s, run %d.\n\n", codeSpan(a.Workflow), a.RunNumber)
	if block := codeBlock(inert(strings.TrimSpace(a.Title + "\n\n" + a.Body))); block != "" {
		b.WriteString(block + "\n\n")
	}
	where := "on AppTown's change page"
	if changeURL != "" {
		where = changeURL
	}
	if a.Candidate == "" {
		b.WriteString("Waiting for approval in AppTown. Checks, approvals and the full change record: " + where + "\n")
		return b.String()
	}
	commit := codeSpan(strs.ShortSHA(a.Candidate))
	fmt.Fprintf(&b, "**Approvals recorded by AppTown for commit %s**\n\n", commit)
	if len(a.Approvals) == 0 {
		b.WriteString("- None.\n")
	}
	for _, v := range a.Approvals {
		if v.Eval != "" {
			fmt.Fprintf(&b, "- Step %s: settled by AppTown eval %s; no person approved it.\n", codeSpan(inert(v.Step)), codeSpan(inert(v.Eval)))
			continue
		}
		fmt.Fprintf(&b, "- Step %s: approved by %s at %s", codeSpan(inert(v.Step)), publicName(v.By, true), when(v.At))
		if v.Candidate != "" {
			b.WriteString(" for commit " + codeSpan(strs.ShortSHA(v.Candidate)))
		}
		b.WriteString(".\n")
		if v.Means != "" {
			b.WriteString("  Meaning they approved: " + codeSpan(inert(v.Means)) + "\n")
		}
		b.WriteString("  Declared approvers: " + declared(v.Declared) + "\n")
		for _, also := range v.AlsoApproves {
			b.WriteString("  Also agreed to: " + codeSpan(inert(also)) + "\n")
		}
	}
	b.WriteString("\n")
	if a.LandingAuthority != "" {
		b.WriteString("Landing started by " + publicName(a.LandingAuthority, true) + ", who held landing authority on this change when the run was admitted.\n\n")
	}
	if a.EvalAuthority != "" {
		b.WriteString("Landing admitted by the authority of AppTown eval " + codeSpan(inert(a.EvalAuthority)) + ", in its copy of the project.\n\n")
	}
	if len(a.Overrides) > 0 {
		b.WriteString("**Overrides**\n\n")
		for _, o := range a.Overrides {
			fmt.Fprintf(&b, "- Step %s overridden by %s: %s\n", codeSpan(inert(o.Step)), publicName(o.By, true), codeSpan(inert(o.Reason)))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "**Checks at commit %s**\n\n", commit)
	if len(a.Checks) == 0 {
		b.WriteString("- None.\n")
	}
	for _, c := range a.Checks {
		fmt.Fprintf(&b, "- %s: %s (AppTown step %d, attempt %d)\n", codeSpan(inert(c.Step)), oneLine(c.Result), c.StepID, c.Attempt)
	}
	b.WriteString("\nAppTown merges this request with its GitLab account on these approvals. Checks, approvals and the full change record: " + where + "\n")
	return b.String()
}

// attributionTrailers are the merge commit's lines about who opened the
// change and what admitted the merge, after AppTown-Run-Id and AppTown-Workflow.
// Each names only what the record holds: Opened-by is the change's creation
// receipt, Approved-by a recorded approving vote with the sentence its step
// declared (no role), Landing-authority the run fact as recorded at
// admission. They are the forge's statements, written by its account, not
// signatures of the people named.
func attributionTrailers(a *Attribution, changeURL string) []string {
	if a == nil {
		return nil
	}
	var lines []string
	add := func(key, value string) {
		lines = append(lines, trailer.Block(trailer.Line{Key: key, Value: oneLine(value)}))
	}
	if !a.ChangeID.IsZero() {
		add(trailer.ChangeID, a.ChangeID.String())
	}
	if a.OpenedBy != "" {
		add(trailer.OpenedBy, publicName(a.OpenedBy, false))
	}
	for _, v := range a.Approvals {
		if v.Eval != "" {
			add(trailer.EvalAuthorised, fmt.Sprintf("step %s by eval %s", inert(v.Step), inert(v.Eval)))
			continue
		}
		value := fmt.Sprintf("%s (step %s, %s)", publicName(v.By, false), inert(v.Step), v.At)
		if v.Means != "" {
			value += ": " + inert(v.Means)
		}
		add(trailer.ApprovedBy, value)
	}
	if a.LandingAuthority != "" {
		add(trailer.LandingAuthority, publicName(a.LandingAuthority, false))
	}
	for _, o := range a.Overrides {
		add(trailer.Override, fmt.Sprintf("%s by %s: %s", inert(o.Step), publicName(o.By, false), inert(o.Reason)))
	}
	if changeURL != "" && !a.ChangeID.IsZero() {
		add(trailer.ChangePage, changeURL)
	}
	return lines
}

// mergeMessage is a landing's merge commit message: its first line, the run
// and workflow, the attribution, and for a local merge the operation that
// inspection finds it by.
func mergeMessage(source Source, operation, changeURL string) string {
	lines := []string{trailer.Block(trailer.Line{Key: trailer.RunID, Value: source.RunID.String()}, trailer.Line{Key: trailer.Workflow, Value: fmt.Sprintf("%s@v%d", source.WorkflowName, source.WorkflowVersion)})}
	lines = append(lines, attributionTrailers(source.Attribution, changeURL)...)
	if operation != "" {
		lines = append(lines, trailer.Block(trailer.Line{Key: trailer.Operation, Value: operation}))
	}
	return fmt.Sprintf("apptown: complete %s run %d\n\n%s", source.WorkflowName, source.RunNumber, strings.Join(lines, "\n"))
}

// withheld stands for an account name that looks like an email address.
// Single sign-on names usually are one, and no such name goes on a public
// page; the change page keeps it for those who may read it.
const withheld = "an AppTown account (name withheld)"

// publicName is how a receipt name appears on GitLab: the forge itself as
// "Forge", a run or eval by number, a person by account name (in a code span
// when code is set, so GitLab never links it to a GitLab user of that name),
// and how they acted when it was through something.
func publicName(receipt string, code bool) string {
	name, via, through := strings.Cut(strings.TrimSpace(receipt), " via ")
	var shown string
	switch {
	case name == "":
		return "nobody recorded"
	case name == "apptown-system" || name == "system":
		shown = "AppTown"
	case strings.HasPrefix(name, "run/") && recordID(name[4:]):
		shown = "AppTown run " + name[4:]
	case strings.HasPrefix(name, "eval/") && recordID(name[5:]):
		shown = "AppTown eval " + name[5:]
	case strings.Contains(name, "@"):
		shown = withheld
	case code:
		shown = codeSpan(inert(name))
	default:
		shown = oneLine(inert(name))
	}
	if !through {
		return shown
	}
	switch {
	case strings.HasPrefix(via, "console/"):
		return shown + " (through a chat)"
	case strings.HasPrefix(via, "mcp/"):
		return shown + " (through a coding agent)"
	case via == "git ticket":
		return shown + " (through a Git push)"
	default:
		return shown + " (through another client)"
	}
}

// recordID is whether s is a record's id in an actor name: a UUID, or a
// number in history from before UUIDs.
func recordID(s string) bool {
	if _, err := ids.Parse(s); err == nil {
		return true
	}
	return digits(s)
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func declared(approvers []string) string {
	if len(approvers) == 0 {
		return "project defaults"
	}
	shown := make([]string, len(approvers))
	for i, a := range approvers {
		shown[i] = declaredApprover(a)
	}
	return strings.Join(shown, ", ")
}

// declaredApprover shows one approver expression. A role is shown as
// declared; an account or group that looks like an email address is
// withheld like any account name.
func declaredApprover(expression string) string {
	if !strings.Contains(expression, "@") {
		return codeSpan(inert(expression))
	}
	if strings.HasPrefix(expression, "group:") {
		return "an AppTown group (name withheld)"
	}
	return withheld
}

func when(at string) string {
	if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
		return t.UTC().Format("2006-01-02 15:04 UTC")
	}
	return codeSpan(at)
}

// clean drops control characters other than newlines and tabs, and makes
// every line break a newline.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r' || r == ' ' || r == ' ' || r == '\u0085':
			return '\n'
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// oneLine is s on one line, runs of white space as one space.
func oneLine(s string) string { return strings.Join(strings.Fields(clean(s)), " ") }

// codeSpan shows text inline as code: GitLab renders it literally, with no
// HTML, references or mentions. Its fence is longer than any run of
// backticks inside.
func codeSpan(s string) string {
	s = oneLine(s)
	if s == "" {
		return "(empty)"
	}
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	return fence + pad + s + pad + fence
}

// codeBlock shows text as an indented code block: every line starts with four
// spaces, so none starts with "/" (GitLab runs quick actions only at the
// start of a line) and GitLab renders the block literally.
func codeBlock(s string) string {
	s = strings.Trim(strings.ReplaceAll(clean(s), "\t", "    "), "\n")
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "    " + strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// inert is text the forge did not write, made so GitLab acts on none of it
// wherever it appears, code included: GitLab scans raw merge request
// descriptions and commit messages for closing statements ("fixes #12",
// "closes https://gitlab.com/g/p/-/issues/12") and closes those issues when
// the merge reaches the default branch, and reads #12, !3 and @name as
// references and mentions. Each #, ! and @ is separated from what follows,
// and every link's scheme from its address, so none of them is a reference.
// Line breaks are kept. The forge's own change-page link never passes here.
func inert(s string) string {
	s = strings.ReplaceAll(clean(s), "://", ":// ")
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		b.WriteRune(r)
		if (r == '#' || r == '!' || r == '@') && i+1 < len(runes) && runes[i+1] != ' ' && runes[i+1] != '\n' {
			b.WriteRune(' ')
		}
	}
	return b.String()
}
