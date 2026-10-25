package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type testTarget struct {
	digest  string
	foreign bool
}

func (t testTarget) RecordTargetIn(ctx context.Context, tx *database.Tx, in TargetChange) (RecordedTarget, error) {
	var id ids.ID
	change := in.ChangeID
	if t.foreign {
		change = idstest.N(999999)
	}
	// sql-guarantee: This target-port double supplies an arbitrary digest, exact candidate JSON and a deliberately wrong change in the foreign-target case; Adoption.RecordTargetIn validates a sealed candidate and cannot produce that inconsistent receipt, and its generated insert is owner-internal.
	err := tx.SQL().QueryRowContext(ctx, `INSERT INTO project_adoptions(id,project_id,revision,change_id,kind,digest,created_by,receipt_json,created_at) VALUES($1,$2,0,$3,'target',$4,$5,'{}',$6) RETURNING id`, ids.New(time.Now()), in.ProjectID, change, t.digest, in.Actor, time.Now()).Scan(&id)
	return RecordedTarget{id, json.RawMessage(`{"expectedRevision":7,"exact":9007199254740993}`)}, err
}
func upliftFixture(t *testing.T) (changeStore, Uplift) {
	t.Helper()
	db := testDB(t)
	id := workflowTestProject(t, db, "uplift", "Uplift", "main")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		for revision := int64(1); revision <= 7; revision++ {
			if err := adoption.RecordIn(t.Context(), tx, id, revision, adoption.AdoptionCreate, "uplift prerequisite", adoption.EmptyReceipt(), "alice", time.Now()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return changeStore{db, clocked{}}, Uplift{ProjectID: id, ExpectedRevision: 7, Words: []string{"demo v2"}, Acceptance: Acceptance{TargetDigest: "exact-digest", Checks: []AcceptanceCheck{{Block: "design", Check: "playable", Version: 9007199254740993, Expects: "historical behavior"}}}, Target: testTarget{digest: "exact-digest"}}
}
func TestSpecializedUpliftCommitsExactTargetAndRequiredAttribution(t *testing.T) {
	t.Parallel()
	c, in := upliftFixture(t)
	ch, err := c.openUplift(context.Background(), in, "alice via mcp/test")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Type != "uplift" || ch.State != ChangeInProgress || ch.Branch != "" || ch.ExpectedRevision != 7 || ch.TargetAdoptionID.IsZero() {
		t.Fatalf("uplift: %+v", ch)
	}
	var acc Acceptance
	if err := json.Unmarshal([]byte(ch.AcceptanceJSON), &acc); err != nil || !reflect.DeepEqual(acc, in.Acceptance) {
		t.Fatalf("acceptance: %+v %v", acc, err)
	}
	// sql-guarantee: project_adoptions belongs to Adoption; the target row's exact encoding (its change, digest and author) is the guarantee.
	completionCount(t, c.db, 1, `SELECT count(*) FROM project_adoptions WHERE id=? AND project_id=? AND revision=0 AND change_id=? AND kind='target' AND digest=? AND created_by=?`, ch.TargetAdoptionID, in.ProjectID, ch.ID, in.Acceptance.TargetDigest, "alice via mcp/test")
	wantJSONField(t, "target candidate", json.RawMessage(storedChange(t, c.db, ch.ID).TargetCandidateJSON), "exact", "9007199254740993")
	wantReceiptCount(t, c.db, 1, inProject(in.ProjectID, receiptWith("change.created", "alice via mcp/test", "changeId", fmt.Sprint(ch.ID))))
	wantReceiptCount(t, c.db, 1, inProject(in.ProjectID, receiptWith("wow.uplift.opened", "alice via mcp/test")))
}
func TestSpecializedUpliftRollsBackEveryRequiredFact(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"project", "insert", "creation receipt", "target", "link", "readback", "foreign target", "suppressed insert", "suppressed target", "suppressed link", "suppressed operation receipt", "deferred target", "deferred operation receipt"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			c, in := upliftFixture(t)
			switch fault {
			case "project":
				dbtest.FailNextStatement(t, c.db, "-- name: ProjectForUpdate")
			case "insert":
				dbtest.FailNextStatement(t, c.db, "INSERT INTO changes")
			case "creation receipt":
				dbtest.FailNextStatement(t, c.db, "INSERT INTO audit_log")
			case "target":
				dbtest.FailNextStatement(t, c.db, "INSERT INTO project_adoptions")
			case "link":
				dbtest.FailNextStatement(t, c.db, "UPDATE changes SET target_adoption_id")
			case "readback":
				dbtest.FailNextStatement(t, c.db, "-- name: ChangeByID")
			case "foreign target":
				in.Target = testTarget{digest: in.Acceptance.TargetDigest, foreign: true}
			default:
				table, event, condition := "changes", "INSERT", "true"
				if strings.Contains(fault, "target") {
					table = "project_adoptions"
				}
				if strings.Contains(fault, "link") {
					event = "UPDATE"
					condition = "NEW.target_adoption_id <> 0"
				}
				if strings.Contains(fault, "receipt") {
					table = "audit_log"
					condition = "NEW.kind = 'wow.uplift.opened'"
				}
				action := "RETURN NULL;"
				trigger := "CREATE TRIGGER veto BEFORE " + event + " ON " + table
				if strings.HasPrefix(fault, "deferred") {
					action = "RAISE EXCEPTION 'reject target settlement';"
					trigger = "CREATE CONSTRAINT TRIGGER veto AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, c.db, `CREATE FUNCTION veto() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN `+action+` END IF; RETURN NEW; END $$`)
				completionExec(t, c.db, trigger+` FOR EACH ROW EXECUTE FUNCTION veto()`)
			}
			ch, err := c.openUplift(context.Background(), in, "alice")
			if ch != nil || err == nil {
				t.Fatalf("partial success: %+v %v", ch, err)
			}
			for _, table := range []string{"changes", "project_adoptions", "audit_log"} {
				// Seven standing revisions and their receipts predate target admission.
				before := 7
				if table == "changes" {
					before = 0
				}
				// sql-guarantee: rollback proves no hidden row in any of the three tables, including Adoption's project_adoptions and the whole audit ledger.
				completionCount(t, c.db, before, "SELECT count(*) FROM "+table)
			}
		})
	}
}
func TestSpecializedUpliftRefusesStaleRevisionAndConcurrentDuplicate(t *testing.T) {
	t.Parallel()
	c, in := upliftFixture(t)
	in.ExpectedRevision = 6
	if ch, err := c.openUplift(context.Background(), in, "alice"); ch != nil || err == nil {
		t.Fatalf("stale: %+v %v", ch, err)
	}
	in.ExpectedRevision = 7
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := c.openUplift(context.Background(), in, "alice"); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("%d duplicate admissions", successes)
	}
	// sql-guarantee: a whole-store total; no workflow read enumerates Changes across every project, and this proves no hidden Change row was written anywhere.
	completionCount(t, c.db, 1, `SELECT count(*) FROM changes`)
	// sql-guarantee: project_adoptions belongs to Adoption; the total proves the duplicate admission wrote no second target revision.
	completionCount(t, c.db, 8, `SELECT count(*) FROM project_adoptions`)
	wantReceiptCount(t, c.db, 9, nil)
}

// importTemplate is the history importFixture gives each test a copy of:
// main with one empty commit by a historical author, and feature-early
// one commit ahead of it. importBase is main's commit in every copy.
var importBase string
var importTemplate = gittest.NewTemplate(func(t testing.TB, repo string) {
	git := func(args ...string) { gittest.Run(t, repo, args...) }
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Historical Author")
	git("config", "user.email", "history@example.test")
	git("commit", "-q", "--allow-empty", "-m", "original main")
	importBase = gittest.Run(t, repo, "rev-parse", "HEAD")
	git("checkout", "-q", "-b", "feature-early")
	if err := os.WriteFile(filepath.Join(repo, "game.txt"), []byte("original playable code\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "game.txt")
	git("commit", "-q", "-m", "original first game")
	git("checkout", "-q", "main")
})

func importFixture(t *testing.T) (changeImporter, ImportBranch, string) {
	t.Helper()
	db := testDB(t)
	id := workflowTestProject(t, db, "history", "History", "main")
	g := GitWorkspace{DataDir: t.TempDir()}
	repo := filepath.Join(g.DataDir, "workspaces", "project-"+id.String())
	importTemplate.CopyTo(t, repo)
	return newImporter(db, g, &scm.RepositoryLocks{}, func(context.Context, CreatedChange) error { return nil }), ImportBranch{ProjectID: id, Branch: "feature-early"}, importBase
}

// An import names its branch from outside: a name git would not take as a
// branch name is refused as invalid, even when a ref of that name exists
// (update-ref takes refs/heads/-early; check-ref-format --branch does not).
func TestImportRefusesANameThatIsNotABranchName(t *testing.T) {
	t.Parallel()
	i, in, _ := importFixture(t)
	repo := filepath.Join(i.git.Paths().DataDir, "workspaces", "project-"+in.ProjectID.String())
	if out, err := gitexec.Run(t.Context(), repo, "update-ref", "refs/heads/-early", "refs/heads/"+in.Branch); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	in.Branch = "-early"
	if ch, err := i.Import(context.Background(), in, "alice"); ch != nil || err == nil || !strings.Contains(err.Error(), "invalid branch") {
		t.Fatalf("import of %q: %+v %v", in.Branch, ch, err)
	}
}
func TestSpecializedImportPreservesHistoricalIdentityAndPostCommitFailure(t *testing.T) {
	t.Parallel()
	i, in, base := importFixture(t)
	failure := errors.New("workspace unavailable")
	calls := 0
	i.creator.prepare = func(ctx context.Context, created CreatedChange) error {
		calls++
		wantReceiptCount(t, i.creator.records.db, 2, inProject(in.ProjectID, nil))
		return failure
	}
	ch, err := i.Import(context.Background(), in, "alice via mcp/history")
	if ch == nil || !errors.Is(err, failure) || calls != 1 || !strings.Contains(err.Error(), "is recorded") {
		t.Fatalf("identity lost: %+v %v calls %d", ch, err, calls)
	}
	if ch.BaseSHA != base || ch.Branch != in.Branch || ch.Provenance != "imported/branch" || ch.Title != "import "+in.Branch {
		t.Fatalf("historical identity changed: %+v", ch)
	}
	wantReceiptCount(t, i.creator.records.db, 1, receiptWith("change.created", "alice via mcp/history", "changeId", fmt.Sprint(ch.ID)))
	wantReceiptCount(t, i.creator.records.db, 1, receiptWith("change.imported", "", "base", base, "branch", in.Branch))
	// Explicit preparation retries the same committed identity; it does not re-import.
	i.creator.prepare = func(context.Context, CreatedChange) error { return nil }
	again, err := i.creator.prepareCreated(context.Background(), CreatedChange{Change: ch, Slug: "history", DefaultBranch: "main"})
	if err != nil || again.ID != ch.ID {
		t.Fatalf("retry: %+v %v", again, err)
	}
	// sql-guarantee: a whole-store total; no workflow read enumerates Changes across every project, and this proves no hidden Change row was written anywhere.
	completionCount(t, i.creator.records.db, 1, `SELECT count(*) FROM changes`)
}
func TestSpecializedImportRequiredReceiptAndSourceChecks(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source", "project", "receipt", "deferred receipt", "suppressed receipt", "suppressed insert"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			i, in, base := importFixture(t)
			db := i.creator.records.db
			source := importSource{in.ProjectID, "history", "main", in.Branch, base, "tip"}
			switch mode {
			case "source":
				source.branch = "different"
			case "project":
				if err := project.New(db, nil).SetDefaultBranch(context.Background(), in.ProjectID, "other"); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
			default:
				table, condition := "audit_log", "NEW.kind = 'change.imported'"
				if mode == "suppressed insert" {
					table, condition = "changes", "true"
				}
				trigger := "CREATE TRIGGER veto BEFORE INSERT ON " + table
				action := "RETURN NULL;"
				if mode == "deferred receipt" {
					trigger = "CREATE CONSTRAINT TRIGGER veto AFTER INSERT ON " + table + " DEFERRABLE INITIALLY DEFERRED"
					action = "RAISE EXCEPTION 'reject import';"
				}
				completionExec(t, db, `CREATE FUNCTION veto() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN `+action+` END IF; RETURN NEW; END $$`)
				completionExec(t, db, trigger+` FOR EACH ROW EXECUTE FUNCTION veto()`)
			}
			got, err := i.creator.records.importBranch(context.Background(), in, source, "alice")
			if err == nil || got.Change != nil {
				t.Fatalf("partial import: %+v %v", got, err)
			}
			// sql-guarantee: a whole-store total; no workflow read enumerates Changes across every project, and this proves no hidden Change row was written anywhere.
			completionCount(t, db, 0, `SELECT count(*) FROM changes`)
			wantReceiptCount(t, db, 0, nil)
		})
	}
}
func TestSpecializedAdmissionCancelsProjectAndRepositoryWaits(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		i, in, _ := importFixture(t)
		release, err := i.locks.Acquire(context.Background(), in.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ch, err := i.Import(ctx, in, "alice")
		if ch != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %+v %v", ch, err)
		}
		// sql-guarantee: a whole-store total; no workflow read enumerates Changes across every project, and this proves no hidden Change row was written anywhere.
		completionCount(t, i.creator.records.db, 0, `SELECT count(*) FROM changes`)
	})
	t.Run("project", func(t *testing.T) {
		t.Parallel()
		c, in := upliftFixture(t)
		gate, err := c.db.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = gate.Rollback() }()
		holdProjectLock(t, gate, in.ProjectID)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := c.openUplift(ctx, in, "alice"); done <- err }()
		dbtest.WaitForLock(t, c.db, "-- name: ProjectForUpdate")
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("project wait did not cancel")
		}
		// sql-guarantee: a whole-store total; no workflow read enumerates Changes across every project, and this proves no hidden Change row was written anywhere.
		completionCount(t, c.db, 0, `SELECT count(*) FROM changes`)
	})
}
