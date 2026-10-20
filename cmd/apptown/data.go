package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"
)

// cmdData is how a project's cloud deployments' data is protected
// (design/subsystems/deployment.md): for each data-holding item, how recent
// AppTown's copies are, the provider's own backups, and when a copy last
// came back; the last restore rehearsal; and the running operation. "copy",
// "check", "rehearse" and "test" start a copy to the backup store, a check
// that the newest copy comes back, a restore rehearsal of the app on it, or
// a restore test on the deployment's own platform behind AppTown gate,
// and follow it to its end. A restored copy is opened from AppTown in a
// browser, never from here.
func cmdData(args []string) error {
	fs := newFlags("data")
	proj := fs.String("p", os.Getenv("APPTOWN_PROJECT"), "project slug (default: $APPTOWN_PROJECT)")
	noWait := fs.Bool("no-wait", false, "print the operation's address and return; follow it with apptown wait operation <id>")
	without := fs.Bool("without-final-copy", false, "take-down: go without a final copy when none can be taken; the data stays at the provider as a retained resource")
	args = parseArgs(fs, args)
	if *proj == "" {
		return fmt.Errorf("no project: pass -p <slug>")
	}
	if len(args) == 2 && args[0] == "take-down" {
		return takeDown(os.Stdout, *proj, args[1], *without)
	}
	if len(args) >= 2 && (args[0] == "copy" || args[0] == "check" || args[0] == "rehearse" || args[0] == "test") {
		what := map[string]string{"copy": "copies", "check": "drills", "rehearse": "rehearsals", "test": "restored-copies"}[args[0]]
		body := map[string]any{}
		if args[0] == "test" {
			body["test"] = true
		}
		var out operationAdmitted
		if err := api.post("/projects/"+*proj+"/deployments/"+args[1]+"/"+what, body, &out); err != nil {
			return err
		}
		if *noWait {
			fmt.Printf("operation %d %s — %s\nfollow it: apptown wait operation %d\n", out.OperationID, out.State, out.URL, out.OperationID)
			return nil
		}
		return followOperation(os.Stdout, out.OperationID, 2*time.Second)
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: apptown data -p <slug> <deployment> · apptown data -p <slug> copy|check|rehearse|test <deployment> · apptown data -p <slug> take-down <deployment> [--without-final-copy]")
	}
	var p protection
	if err := api.get("/projects/"+*proj+"/deployments/"+args[0]+"/protection", &p); err != nil {
		return err
	}
	printProtection(os.Stdout, p)
	return nil
}

// operationAdmitted is an owner door's 202: the operation and where to
// follow it.
type operationAdmitted struct {
	OperationID ids.ID `json:"operationId"`
	State       string `json:"state"`
	URL         string `json:"url"`
}

// takeDown takes a deployment down. A deployment holding data AppTown
// cannot take a final copy of now is refused with why, unless the person
// passed --without-final-copy, which names the deployment to AppTown as
// their choice; an App Platform take-down with a final copy is followed to
// its end.
func takeDown(w io.Writer, proj, name string, without bool) error {
	body := map[string]any{}
	if without {
		body["withoutFinalCopy"] = name
	}
	raw, err := api.request("DELETE", "/projects/"+proj+"/deployments/"+name, body)
	if err != nil {
		return err
	}
	// An App Platform take-down with a final copy is an operation: 202
	// with its handle.
	var out struct {
		OperationID ids.ID   `json:"operationId"`
		Deleted     []string `json:"deleted"`
		Orphans     []string `json:"orphans"`
		FinalBackup *struct {
			ID ids.ID `json:"id"`
		} `json:"finalBackup"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if !out.OperationID.IsZero() {
		return followOperation(w, out.OperationID, 2*time.Second)
	}
	fmt.Fprintf(w, "%s taken down\n", name)
	for _, d := range out.Deleted {
		fmt.Fprintf(w, "  deleted %s\n", d)
	}
	if out.FinalBackup != nil {
		fmt.Fprintf(w, "  final backup: set %s\n", out.FinalBackup.ID)
	}
	for _, o := range out.Orphans {
		fmt.Fprintf(w, "  kept, under retained resources: %s\n", o)
	}
	return nil
}

// dataOperation is one operation as GET /operations/{id} answers it.
type dataOperation struct {
	ID           ids.ID         `json:"id"`
	Kind         string         `json:"kind"`
	State        string         `json:"state"`
	Verification string         `json:"verification"`
	Detail       string         `json:"detail"`
	AskedBy      string         `json:"askedBy"`
	CreatedAt    string         `json:"createdAt"`
	StartedAt    string         `json:"startedAt"`
	Result       map[string]any `json:"result"`
	Steps        []struct {
		Name    string  `json:"name"`
		Started string  `json:"started"`
		Ended   *string `json:"ended"`
		Failed  bool    `json:"failed"`
		Unknown bool    `json:"unknown"`
		Detail  string  `json:"detail"`
	} `json:"steps"`
}

// writesStopped says a live take-down has deleted the app.
func (op dataOperation) writesStopped() bool {
	stopped, _ := op.Result["writesStopped"].(bool)
	return stopped && (op.State == "queued" || op.State == "running")
}

type protection struct {
	Deployment string `json:"deployment"`
	DataClass  string `json:"dataClass"`
	OffHost    bool   `json:"offHost"`
	Note       string `json:"note"`
	Operations *struct {
		Copy struct {
			Available bool   `json:"available"`
			Why       string `json:"why"`
		} `json:"copy"`
	} `json:"operations"`
	StartFrom *struct {
		Backup ids.ID `json:"backup"`
		Filled string `json:"filled"`
	} `json:"startFrom"`
	Items []struct {
		Need     string `json:"need"`
		Where    string `json:"where"`
		Copy     string `json:"copy"`
		CopyNote string `json:"copyNote"`
		LastCopy *struct {
			Backup  ids.ID `json:"backup"`
			TakenAt string `json:"takenAt"`
			Bytes   int64  `json:"bytes"`
		} `json:"lastCopy"`
		Native *struct {
			Mechanism string `json:"mechanism"`
			Count     int    `json:"count"`
			Oldest    string `json:"oldest"`
			Newest    string `json:"newest"`
			Unknown   string `json:"unknown"`
		} `json:"native"`
		Drill *struct {
			At     string `json:"at"`
			Backup ids.ID `json:"backup"`
			Place  string `json:"place"`
			User   string `json:"user"`
			Passed bool   `json:"passed"`
			Detail string `json:"detail"`
		} `json:"drill"`
	} `json:"items"`
	Running   *dataOperation `json:"running"`
	Rehearsal *struct {
		At         string `json:"at"`
		Outcome    string `json:"outcome"`
		Passed     bool   `json:"passed"`
		Backup     ids.ID `json:"backup"`
		Release    int64  `json:"release"`
		Host       string `json:"host"`
		FailedStep string `json:"failedStep"`
		Operation  ids.ID `json:"operation"`
		Facts      struct {
			Withheld   []string `json:"withheld"`
			NotStarted []string `json:"notStarted"`
			Egress     string   `json:"egress"`
		} `json:"facts"`
	} `json:"rehearsal"`
	RehearsalNote   string         `json:"rehearsalNote"`
	RestoreTest     *restoredCopy  `json:"restoreTest"`
	RestoreTestNote string         `json:"restoreTestNote"`
	RestoredCopies  []restoredCopy `json:"restoredCopies"`
	Coverage        struct {
		Assessed    []string `json:"assessed"`
		NotAssessed []string `json:"notAssessed"`
	} `json:"coverage"`
}

type restoredCopy struct {
	ID         ids.ID `json:"id"`
	Number     int64  `json:"number"`
	Deployment string `json:"deployment"`
	Backup     ids.ID `json:"backup"`
	Release    int64  `json:"release"`
	Target     string `json:"target"`
	Test       bool   `json:"test"`
	StartedAt  string `json:"startedAt"`
	ExpiresAt  string `json:"expiresAt"`
	CheckedAt  string `json:"checkedAt"`
	EndedAt    string `json:"endedAt"`
	EndDetail  string `json:"endDetail"`
	Open       bool   `json:"open"`
	Outcome    string `json:"outcome"`
	FailedStep string `json:"failedStep"`
	Warning    string `json:"warning"`
	Entries    []struct {
		Person string `json:"person"`
	} `json:"entries"`
	Facts struct {
		Gate []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Status int    `json:"status"`
			Passed bool   `json:"passed"`
		} `json:"gate"`
	} `json:"facts"`
}

// gateLine is AppTown's own check of a copy's gate, as the web card says
// it: how many checks passed, and each that failed with what it answered;
// "" when the gate was not checked.
func (c restoredCopy) gateLine() string {
	if len(c.Facts.Gate) == 0 {
		return ""
	}
	var failed []string
	for _, k := range c.Facts.Gate {
		if !k.Passed {
			failed = append(failed, fmt.Sprintf("%s (%s %d)", k.Name, k.Path, k.Status))
		}
	}
	line := fmt.Sprintf("gate: %d of %d checks passed", len(c.Facts.Gate)-len(failed), len(c.Facts.Gate))
	if len(failed) > 0 {
		line += "; failed: " + strings.Join(failed, ", ")
	}
	return line
}

func printProtection(w io.Writer, p protection) {
	fmt.Fprintf(w, "%s (%s data)\n", bold(p.Deployment), p.DataClass)
	if p.Note != "" {
		fmt.Fprintf(w, "  %s\n", p.Note)
	}
	// AppTown's own sentence for why a copy cannot start, as "apptown data
	// copy" would be refused with.
	if o := p.Operations; o != nil && !o.Copy.Available {
		fmt.Fprintf(w, "  %s\n", o.Copy.Why)
	}
	if s := p.StartFrom; s != nil {
		if s.Filled != "" {
			fmt.Fprintf(w, "  started from set %s, filled %s\n", s.Backup, s.Filled)
		} else {
			fmt.Fprintf(w, "  starts from set %s on its first release\n", s.Backup)
		}
	}
	for _, it := range p.Items {
		fmt.Fprintf(w, "  %s — %s\n", it.Need, it.Where)
		line := "    copies " + it.Copy
		if it.LastCopy != nil {
			line += fmt.Sprintf(": newest set %s, %s, %d bytes", it.LastCopy.Backup, it.LastCopy.TakenAt, it.LastCopy.Bytes)
		}
		if it.CopyNote != "" {
			line += " (" + it.CopyNote + ")"
		}
		fmt.Fprintln(w, line)
		if n := it.Native; n != nil {
			switch {
			case n.Unknown != "":
				fmt.Fprintf(w, "    %s: unknown — %s\n", n.Mechanism, n.Unknown)
			case n.Count == 0:
				fmt.Fprintf(w, "    %s: none yet\n", n.Mechanism)
			default:
				fmt.Fprintf(w, "    %s: %d, oldest %s, newest %s\n", n.Mechanism, n.Count, n.Oldest, n.Newest)
			}
		}
		switch d := it.Drill; {
		case d == nil:
			fmt.Fprintln(w, "    never checked to come back")
		case d.Passed:
			// Where it came back is the place it was filled, not the
			// deployment's target; a bucket's new store is filled as no
			// app user.
			line := fmt.Sprintf("    came back on %s from set %s into %s", d.At, d.Backup, d.Place)
			if d.User != "" {
				line += " as " + d.User
			}
			fmt.Fprintln(w, line)
		default:
			fmt.Fprintf(w, "    did not come back on %s from set %s: %s\n", d.At, d.Backup, d.Detail)
		}
	}
	// What the copies, checks and restores above speak for, and what they
	// do not: data outside the deployment is never assessed.
	covers := "no data AppTown knows of"
	if len(p.Coverage.Assessed) > 0 {
		covers = strings.Join(p.Coverage.Assessed, ", ")
	}
	fmt.Fprintf(w, "  covers: %s\n", covers)
	for _, n := range p.Coverage.NotAssessed {
		fmt.Fprintf(w, "  not assessed: %s\n", n)
	}
	switch r := p.Rehearsal; {
	case r == nil:
		fmt.Fprintf(w, "  last restore rehearsal: %s\n", p.RehearsalNote)
	case strings.HasPrefix(r.Outcome, "unknown"):
		fmt.Fprintf(w, "  last restore rehearsal %s: %s (operation %d)\n", r.At, r.Outcome, r.Operation)
	case r.Passed:
		fmt.Fprintf(w, "  last restore rehearsal: %s %s from set %s, release %d, on %s\n", r.Outcome, r.At, r.Backup, r.Release, r.Host)
	default:
		fmt.Fprintf(w, "  last restore rehearsal: failed %s from set %s, release %d, on %s, while %s\n", r.At, r.Backup, r.Release, r.Host, r.FailedStep)
	}
	if r := p.Rehearsal; r != nil {
		held := []string{r.Facts.Egress}
		if len(r.Facts.Withheld) > 0 {
			held = append(held, "withheld: "+strings.Join(r.Facts.Withheld, ", "))
		}
		if len(r.Facts.NotStarted) > 0 {
			held = append(held, "not started: "+strings.Join(r.Facts.NotStarted, ", "))
		}
		fmt.Fprintf(w, "    %s\n", strings.Join(held, "; "))
	}
	switch c := p.RestoreTest; {
	case c == nil:
		fmt.Fprintf(w, "  last restore test: %s\n", p.RestoreTestNote)
	case c.Outcome == "":
		fmt.Fprintf(w, "  last restore test %s: %s (set %s on %s)\n", c.StartedAt, c.EndDetail, c.Backup, c.Target)
	case c.Outcome == "failed":
		fmt.Fprintf(w, "  last restore test: failed %s from set %s, release %d, on %s, while %s\n", c.CheckedAt, c.Backup, c.Release, c.Target, c.FailedStep)
	default:
		fmt.Fprintf(w, "  last restore test: %s %s from set %s, release %d, on %s\n", c.Outcome, c.CheckedAt, c.Backup, c.Release, c.Target)
	}
	if c := p.RestoreTest; c != nil && c.gateLine() != "" {
		fmt.Fprintf(w, "    %s\n", c.gateLine())
	}
	for _, c := range p.RestoredCopies {
		if c.EndedAt != "" || c.Test {
			continue
		}
		var who []string
		seen := map[string]bool{}
		for _, e := range c.Entries {
			if !seen[e.Person] {
				seen[e.Person] = true
				who = append(who, e.Person)
			}
		}
		opened := "not opened yet"
		if len(who) > 0 {
			opened = "opened by " + strings.Join(who, ", ")
		}
		state := "starting"
		if c.CheckedAt != "" {
			state = "up until " + c.ExpiresAt
			if c.Open {
				state = "open until " + c.ExpiresAt
			}
		}
		fmt.Fprintf(w, "  restored copy %d of %s from set %s · %s · %s (open it from AppTown in a browser)\n", c.Number, p.Deployment, c.Backup, state, opened)
		fmt.Fprintf(w, "    %s\n", c.Warning)
	}
	if op := p.Running; op != nil {
		cur := "between steps"
		if op.State == "queued" {
			cur = "waiting to start"
		} else if n := len(op.Steps); n > 0 && op.Steps[n-1].Ended == nil {
			cur = op.Steps[n-1].Name
		}
		stopped := ""
		if op.writesStopped() {
			stopped = " (writes stopped)"
		}
		fmt.Fprintf(w, "  %s: %s %d, asked by %s at %s — %s%s\n", op.State, op.Kind, op.ID, op.AskedBy, op.CreatedAt, cur, stopped)
	}
}

// followOperation prints an operation's steps as they happen until it
// ends. It returns nil when it succeeded, an error when it failed or was
// cancelled (exit 1), and operationUnknown when how it ended is not known
// (exit 4, as for any outcome that is unknown).
func followOperation(w io.Writer, id ids.ID, interval time.Duration) error {
	op, err := watchOperation(w, id, interval)
	if err != nil {
		return err
	}
	return operationEnding(op)
}

// watchOperation prints an operation's steps as they happen and answers
// the record it ended with. An error is a read that failed: how the
// operation stands is then not observed.
func watchOperation(w io.Writer, id ids.ID, interval time.Duration) (*dataOperation, error) {
	printed := map[int]bool{}
	for {
		var op dataOperation
		if err := api.get(fmt.Sprintf("/operations/%d", id), &op); err != nil {
			return nil, err
		}
		for i, st := range op.Steps {
			if st.Ended == nil || printed[i] {
				continue
			}
			printed[i] = true
			mark := "✓"
			switch {
			case st.Unknown:
				mark = "?"
			case st.Failed:
				mark = "✗"
			}
			detail := ""
			if st.Detail != "" {
				detail = " — " + st.Detail
			}
			fmt.Fprintf(w, "  %s %s%s\n", mark, st.Name, detail)
		}
		switch op.State {
		case "queued", "running":
			pause(interval)
			continue
		case "succeeded":
			how := op.State
			if op.Verification != "" {
				how += ", " + op.Verification
			}
			fmt.Fprintf(w, "%s %s %d %s: %s\n", bold("▶"), op.Kind, op.ID, how, op.Detail)
		default:
			fmt.Fprintf(w, "%s %s %d %s: %s\n", bold("▶"), op.Kind, op.ID, op.State, op.Detail)
		}
		return &op, nil
	}
}

// operationEnding is how an ended operation's record decides the exit:
// nil when it succeeded, operationUnknown when its effect is not known, an
// error otherwise.
func operationEnding(op *dataOperation) error {
	switch op.State {
	case "succeeded":
		return nil
	case "unknown":
		return &operationUnknown{ID: op.ID, Kind: op.Kind}
	}
	return fmt.Errorf("%s %d ended %s", op.Kind, op.ID, op.State)
}

// operationUnknown: an operation ended, and whether its effect happened is
// not known. The CLI exits with exitUnknown, as for a mutation whose
// answer was lost.
type operationUnknown struct {
	ID   ids.ID
	Kind string
}

func (e *operationUnknown) Error() string {
	return fmt.Sprintf("%s %d: how it ended is not known — look at what it names before asking again", e.Kind, e.ID)
}
