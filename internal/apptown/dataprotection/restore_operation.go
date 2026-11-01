package dataprotection

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// A restore of a backup set over a Docker host's or a Portainer
// deployment (restoreBackup) is an operation, like the other data
// operations: the door checks and admits it, the operations worker runs
// it once, and each step and fact is written as it happens. An AppTown that
// stops part way leaves it running; the restart settles it unknown with
// what it had confirmed. AppTown does not start again what it may have
// stopped, and names it (restoreInterrupted): a restore on the host may
// outlive AppTown and still be writing, so AppTown never starts the
// app on a guess. A person checks the host and releases the deployment
// again. A stop is recorded before it is issued and a start once it succeeded, so what the
// record names may be stopped; its state now is not known.

// operationRestoreSet is a restore of a backup set over a deployment.
const operationRestoreSet = "deployment.restore"

// errRestoreSetNotStopped refuses to stop a restore part way: stopping
// between items would leave the data half old and half new.
var errRestoreSetNotStopped = fault.New(fault.Conflict, "a restore that has started runs to its end and starts the app again after its last item; it can be cancelled only while it waits to start")

// restorable is what a restore of a set into a deployment needs before
// anything changes, asked at the door and again by the operation: an
// active project, a restorable set of the project's that is not a managed
// database's copy, and a standing deployment. It returns the set and the
// deployment.
func (s *Service) restorable(ctx context.Context, projectID ids.ID, name string, backupID ids.ID) (*Backup, *deployment.Deployment, error) {
	if err := project.RefuseUnlessActive(ctx, s.db, projectID); err != nil {
		return nil, nil, err
	}
	b, err := s.backups.inProject(ctx, projectID, backupID)
	if err != nil {
		return nil, nil, fault.New(fault.NotFound, fmt.Sprintf("no backup %d in this project", backupID))
	}
	if err := b.Status.restorable(); err != nil {
		return nil, nil, fmt.Errorf("backup %d: %w", backupID, err)
	}
	// A managed database's copy is only ever filled into a new place: a
	// new deployment starts from it. A Docker host's set is restored in
	// place wherever it is kept.
	for _, it := range b.Items {
		if it.Instance != "" {
			return nil, nil, fmt.Errorf("%w: set %d is a copy of a managed database; start a new deployment from it instead", errRestoreMismatch, backupID)
		}
	}
	if _, err := project.ByIDIn(ctx, s.db, projectID); err != nil {
		return nil, nil, fault.New(fault.NotFound, "no such project")
	}
	dep, err := s.deployments.ByName(ctx, projectID, name)
	if err != nil || !dep.Standing() {
		return nil, nil, fault.New(fault.NotFound, fmt.Sprintf("no standing deployment named %q", name))
	}
	return b, dep, nil
}

// RequestRestore starts a restore of a set over a standing deployment, as
// the caller, who needs the deploy grant in the project. What would refuse
// the restore before any change (no such set or deployment, a set whose
// items have nowhere certain to go) refuses here, before anything is
// recorded. It holds the deployment's data while it lives: a second
// restore, a copy or a take-down operation of it is refused meanwhile; a
// release is not.
func (s *Service) RequestRestore(ctx context.Context, projectID ids.ID, name string, backupID ids.ID) (*operations.Operation, error) {
	actor, err := s.requireAuthority(ctx, access.CapDeploymentDeploy, projectID, operationRestoreSet)
	if err != nil {
		return nil, err
	}
	b, dep, err := s.restorable(ctx, projectID, name, backupID)
	if err != nil {
		return nil, err
	}
	t, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, fmt.Errorf("%s: reading its deployment target: %w", name, err)
	}
	held, err := s.heldData(ctx, dep, t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if _, _, err := restoreMoves(b, held.items); err != nil {
		return nil, err
	}
	return s.admit(ctx, dep, operationRestoreSet, actor, []string{deployment.DataHold(dep)}, map[string]any{"backup": backupID}, nil)
}

// runRestoreSet is the restore operation's body. It ends failed when it
// refused before writing anything, when an item failed or authority was
// withdrawn, and when a part it stopped would not start again (the data
// is back but the app may not be serving); otherwise succeeded,
// verified when every item it put back was verified.
func (s *Service) runRestoreSet(ctx context.Context, t *tracker) (runEnd, string, map[string]any) {
	rec := &restoreLog{t: t}
	res, err := s.restoreBackup(ctx, rec, t.op.ProjectID, t.op.Deployment, inputID(t.op.Input, "backup"), t.op.AskedBy)
	if res == nil {
		why := err.Error()
		if left := mayBeStoppedWords(t.facts()); left != "" {
			why += ". " + left
		}
		return operationFailed, why + rec.lostWords(), nil
	}
	result := map[string]any{"restore": res}
	detail := restoreWords(res) + rec.lostWords()
	whole := len(res.NotStarted) == 0
	if left := mayBeStoppedWords(t.facts()); left != "" {
		detail += " " + left
		whole = false
	}
	if err != nil {
		return operationFailed, err.Error() + ". " + detail, result
	}
	// The data is back but the app is not whole: not a success.
	if !whole {
		return operationFailed, detail, result
	}
	for _, it := range res.Items {
		if it.Outcome != restoreVerified && it.Outcome != restoreSkipped {
			return operationNotVerified, detail, result
		}
	}
	return operationVerified, detail, result
}

// restoreWords says what a restore that ran to its end did.
func restoreWords(r *RestoreResult) string {
	out := fmt.Sprintf("Set %d (from %s) %s: %s.", r.Backup, r.From, r.Status, itemWords(r.Items))
	if r.SafetyBackup != ids.Nil {
		out += fmt.Sprintf(" The safety set %d holds what was there before.", r.SafetyBackup)
	}
	switch {
	case len(r.NotStarted) > 0:
		out += " These are not running again, so the app may not be serving: " + strings.Join(r.NotStarted, "; ") + "."
	case r.Started == nil || !r.Stopped:
		out += " The app was not running, so it was not started."
	case *r.Started && r.Ready != nil && !*r.Ready:
		out += " The app is started again; it did not answer within 90 s."
	case *r.Started:
		out += " The app is started again."
	}
	return out
}

// itemWords is each item and what witnessed it.
func itemWords(items []RestoredItem) string {
	if len(items) == 0 {
		return "no item"
	}
	words := make([]string, 0, len(items))
	for _, it := range items {
		words = append(words, it.Name+" "+it.Outcome.String())
	}
	return strings.Join(words, ", ")
}

// mayBeStopped is what a restore's recorded facts say it stopped, or was
// stopping, and was not seen to start again, in the order it stopped
// them: these may be stopped; whether they are is not known.
func mayBeStopped(facts map[string]any) []string {
	started := stringsOf(facts["started"])
	var left []string
	for _, id := range stringsOf(facts["stopped"]) {
		if !slices.Contains(started, id) && !slices.Contains(left, id) {
			left = append(left, id)
		}
	}
	return left
}

// mayBeStoppedWords says what a restore may have left stopped, and what a
// person does about it; "" when it stopped nothing it did not start again.
func mayBeStoppedWords(facts map[string]any) string {
	left := mayBeStopped(facts)
	if len(left) == 0 {
		return ""
	}
	return strings.Join(left, ", ") + " may be stopped: this restore stopped or was stopping them and was not seen to start them again; their state now is not known. " +
		"A restore on the host may still be writing: check the host for one and look at the containers, then release the deployment again."
}

// restoreInterrupted is what an AppTown's stop left of a restore, from what
// it recorded: the restart repair adds it to the unknown detail.
func restoreInterrupted(op operations.Operation) string {
	facts := op.Result
	var out []string
	if items := itemsOf(facts["items"]); len(items) > 0 {
		out = append(out, "Items restored before it stopped: "+itemWords(items)+".")
	}
	if len(itemsOf(facts["items"])) == 0 {
		out = append(out, "No item was confirmed restored.")
	}
	if left := mayBeStoppedWords(facts); left != "" {
		out = append(out, left)
	} else if _, stopped := facts["stopped"]; !stopped {
		out = append(out, "It stopped no container.")
	}
	if id := inputID(facts, "safetyBackup"); id != ids.Nil {
		out = append(out, fmt.Sprintf("The safety set %d holds what was there before.", id))
	}
	return strings.Join(out, " ")
}

// settleRestore writes the restored receipt in the transaction that ends
// the operation: the restore's outcome, or, when AppTown stopped part
// way, status unknown with the items recorded so far and what may be
// stopped. A restore refused before it changed anything writes none.
func (s *Service) settleRestore(ctx context.Context, tx *database.Tx, op operations.Operation, out operations.Outcome) error {
	facts := op.Result
	payload := map[string]any{"operation": op.ID, "backup": inputID(op.Input, "backup"), "from": facts["from"], "safetyBackup": facts["safetyBackup"]}
	var res RestoreResult
	if raw, ok := facts["restore"]; ok && decode(raw, &res) == nil {
		payload["status"], payload["items"] = res.Status, res.Items
		// Cut off part way (a shutdown), what the data holds is not
		// known, whatever the items it confirmed say.
		if out.State == operations.Unknown {
			payload["status"] = "unknown"
		}
	} else if _, stopped := facts["stopped"]; stopped || len(itemsOf(facts["items"])) > 0 {
		payload["status"], payload["items"] = "unknown", append([]RestoredItem{}, itemsOf(facts["items"])...)
	} else {
		return nil
	}
	if left := mayBeStopped(facts); len(left) > 0 {
		payload["mayBeStopped"] = left
	}
	name, _ := op.Input["deployment"].(string)
	return audit.At(s.now()).RecordIn(ctx, tx, op.AskedBy, op.ProjectID, "restored", name, payload)
}

// restoreLog is how a restore writes as it goes: its steps, its facts,
// each container it stops before the stop and each it starts once started,
// and the check of its authority before each effect. A nil log (the body
// run without an operation, in its own tests) writes nothing and allows.
type restoreLog struct {
	t                   *tracker
	stopped, startedIDs []string
	// lost is the first fact that could not be written: the record may be
	// behind what happened, and the restore's end says so.
	lost error
}

func (l *restoreLog) begin(ctx context.Context, name string) {
	if l != nil {
		l.t.begin(ctx, name)
	}
}

func (l *restoreLog) end(ctx context.Context, detail string) {
	if l != nil {
		l.t.end(ctx, detail, 0)
	}
}

func (l *restoreLog) fail(ctx context.Context, why string) {
	if l != nil {
		l.t.fail(ctx, why)
	}
}

// record writes a fact; one that cannot be written is kept as lost, and
// the restore's end says its record may be behind.
func (l *restoreLog) record(ctx context.Context, key string, v any) {
	if l == nil {
		return
	}
	if err := l.t.record(ctx, key, v); err != nil && l.lost == nil {
		l.lost = err
	}
}

func (l *restoreLog) allowed(ctx context.Context) error {
	if l == nil {
		return nil
	}
	return l.t.allowed(ctx)
}

// stopping records a container before it is stopped; when that cannot be
// written it is not stopped, so nothing is ever stopped off the record.
func (l *restoreLog) stopping(ctx context.Context, id string) error {
	if l == nil {
		return nil
	}
	l.stopped = append(l.stopped, id)
	if err := l.t.record(ctx, "stopped", l.stopped); err != nil {
		l.stopped = l.stopped[:len(l.stopped)-1]
		return fmt.Errorf("could not record that %s is to be stopped, so it was not: %w", id, err)
	}
	return nil
}

// started records a container started again.
func (l *restoreLog) started(ctx context.Context, id string) {
	if l == nil {
		return
	}
	l.startedIDs = append(l.startedIDs, id)
	l.record(ctx, "started", l.startedIDs)
}

// lostWords says the record may be behind, "" when every fact was written.
func (l *restoreLog) lostWords() string {
	if l == nil || l.lost == nil {
		return ""
	}
	return " (some of its facts were not recorded as they happened, so the record may be behind: " + l.lost.Error() + ")"
}

// stringsOf reads a recorded list of strings: as written ([]string) or
// as read back from the ledger ([]any).
func stringsOf(v any) []string {
	switch v := v.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if s, ok := s.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// itemsOf reads the recorded items, as written or as read back.
func itemsOf(v any) []RestoredItem {
	if items, ok := v.([]RestoredItem); ok {
		return items
	}
	var items []RestoredItem
	if v != nil && decode(v, &items) != nil {
		return nil
	}
	return items
}

// decode reads a recorded value into its type through JSON, as the ledger
// keeps it.
func decode(v, into any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}
