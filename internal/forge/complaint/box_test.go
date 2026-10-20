package complaint

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/complaint/internal/complaintsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
)

// The three states are derived from the record, never stored.
func TestStateIsDerived(t *testing.T) {
	if StateOf("", false) != New || StateOf("fixed", false) != Answered || StateOf("fixed", true) != Archived || StateOf("", true) != Archived {
		t.Fatal("state rule wrong")
	}
	if New.String() != "new" || State(0).String() != "invalid-complaint-state" {
		t.Fatal("state words wrong")
	}
}

// A complaint is filed inert, listed with its project, answered with a
// receipt, archived, and deleted with a receipt; blanks are refused.
func TestBoxLifecycle(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	box := NewBox(db, nil, nil)
	if _, err := box.File(t.Context(), Entry{Text: "  "}); !errors.Is(err, ErrText) {
		t.Fatalf("blank text must be refused: %v", err)
	}
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := box.File(t.Context(), Entry{ProjectID: pid, Source: "human", Actor: "bls", Text: "  the gate lied  ", Why: "x", Wish: "y"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := box.All(context.Background(), 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %d", err, len(all))
	}
	c := all[0]
	if c.ID != id || c.Text != "the gate lied" || c.Project != "demo" || c.ProjectName != "Demo" || c.State != "new" || c.Seen {
		t.Fatalf("filed complaint wrong: %+v", c)
	}
	if c.RunNumber != 0 || c.ChangeNumber != 0 {
		t.Fatalf("a complaint naming no run or change has no numbers: %+v", c)
	}
	// A complaint filed in a run names the run and its change by the numbers
	// people read.
	changeID, runID := idstest.N(7001), idstest.N(7002)
	// sql-guarantee: Work owns changes and runs and its writers are internal to it; the row carries only what the listing joins.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO changes (id, number, project_id, type, title, created_at) VALUES ($1, 12, $2, 'feature', 'x', $3)`, changeID, pid, time.Now()); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: as above, for the run.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO runs (id, number, project_id, change_id, workflow_name, workflow_version, kind, current_stage, definition_json, created_at, updated_at) VALUES ($1, 40, $2, $3, 'w', 1, 'pipeline', 's', '{}', $4, $4)`, runID, pid, changeID, time.Now()); err != nil {
		t.Fatal(err)
	}
	inRun, err := box.File(t.Context(), Entry{ProjectID: pid, Source: "run", RunID: runID, ChangeID: changeID, Text: "the step hung"})
	if err != nil {
		t.Fatal(err)
	}
	all, err = box.All(context.Background(), 10)
	if err != nil || len(all) != 2 || all[0].ID != inRun || all[0].RunNumber != 40 || all[0].ChangeNumber != 12 {
		t.Fatalf("a complaint in a run names its numbers: %+v %v", all, err)
	}
	if err := box.Delete(context.Background(), inRun, "ops"); err != nil {
		t.Fatal(err)
	}
	if err := box.Answer(context.Background(), id, " ", "ops"); !errors.Is(err, ErrAnswer) {
		t.Fatalf("blank answer must be refused: %v", err)
	}
	if err := box.Answer(context.Background(), idstest.N(9999), "no", "ops"); !errors.Is(err, ErrMissing) {
		t.Fatalf("unknown complaint: %v", err)
	}
	if err := box.Answer(context.Background(), id, "fixed in #12", "ops"); err != nil {
		t.Fatal(err)
	}
	all, _ = box.All(context.Background(), 10)
	if all[0].State != "answered" || !all[0].Seen || all[0].AnsweredBy != "ops" {
		t.Fatalf("answered complaint wrong: %+v", all[0])
	}
	if n := receipts(t, db, "complaint.answered", id.String()); n != 1 {
		t.Fatalf("answer receipt: %d", n)
	}
	if err := box.Archive(context.Background(), id, "ops"); err != nil {
		t.Fatal(err)
	}
	if err := box.Archive(context.Background(), id, "ops"); !errors.Is(err, ErrMissing) {
		t.Fatalf("archiving twice: %v", err)
	}
	all, _ = box.All(context.Background(), 10)
	if all[0].State != "archived" {
		t.Fatalf("archived complaint wrong: %+v", all[0])
	}
	if err := box.Delete(context.Background(), id, "ops"); err != nil {
		t.Fatal(err)
	}
	if err := box.Delete(context.Background(), id, "ops"); !errors.Is(err, ErrMissing) {
		t.Fatalf("deleting twice: %v", err)
	}
	if n := receipts(t, db, "complaint.deleted", id.String()); n != 1 {
		t.Fatalf("delete receipt: %d", n)
	}
	if all, _ = box.All(context.Background(), 10); len(all) != 0 {
		t.Fatal("deleted complaint still listed")
	}
}

// The inbox sweep archives everything older than the window; the
// ninety-day sweep deletes whatever its state and leaves one line.
func TestBoxSweeps(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	box := NewBox(db, nil, nil)
	old := time.Now().Add(-100 * 24 * time.Hour).UTC()
	yesterday := time.Now().Add(-30 * time.Hour).UTC()
	for i, at := range []time.Time{old, yesterday, time.Now().UTC()} {
		if _, err := complaintsql.New(db.SQL()).InsertComplaint(context.Background(), complaintsql.InsertComplaintParams{ID: ids.New(at), Number: int64(i + 1), At: at, Source: "human", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := box.MarkSeen(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := box.ArchiveOlder(context.Background(), 24*time.Hour, "ops")
	if err != nil || n != 2 {
		t.Fatalf("archive older: %v %d", err, n)
	}
	all, _ := box.All(context.Background(), 10)
	if all[0].State != "new" || all[1].State != "archived" || all[2].State != "archived" || !all[0].Seen {
		t.Fatalf("after the inbox sweep: %+v", all)
	}
	n, err = box.Expire(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("expire: %v %d", err, n)
	}
	if all, _ = box.All(context.Background(), 10); len(all) != 2 {
		t.Fatalf("expire kept %d", len(all))
	}
	if n := receipts(t, db, "complaints.expired", "1"); n != 1 {
		t.Fatalf("expire receipt: %d", n)
	}
	if n, _ := box.Expire(context.Background()); n != 0 {
		t.Fatal("a second sweep removes nothing")
	}
	if n := receipts(t, db, "complaints.expired", "0"); n != 0 {
		t.Fatal("an empty sweep leaves no line")
	}
}

func receipts(t *testing.T, db *database.DB, kind, subject string) int {
	t.Helper()
	lines, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Subject: subject, Limit: audit.MaxLimit})
	if err != nil || len(lines) == audit.MaxLimit {
		t.Fatalf("receipts %d: %v", len(lines), err)
	}
	n := 0
	for _, line := range lines {
		if line.Kind == kind {
			n++
		}
	}
	return n
}

// A credential pasted into a complaint inside a URL is not stored: the user
// part and token-like query values are replaced, and the rest is kept.
func TestFilingRedactsCredentialsInURLs(t *testing.T) {
	t.Parallel()
	box := NewBox(testDB(t), nil, nil)
	_, err := box.File(t.Context(), Entry{Source: "console", Actor: "agent",
		Text: "fetch from https://x-token:abc123@forge.example/git/app failed",
		Why:  "see http://host/api?access_token=s3cr3t&page=2",
		Wish: "git://user@host/repo"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := box.All(context.Background(), 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %d", err, len(all))
	}
	c := all[0]
	if c.Text != "fetch from https://redacted@forge.example/git/app failed" ||
		c.Why != "see http://host/api?access_token=redacted&page=2" ||
		c.Wish != "git://redacted@host/repo" {
		t.Fatalf("credentials kept: %+v", c)
	}
}

type packsFor map[ids.ID]string

func (p packsFor) PackVersionsOf(_ context.Context, projectID ids.ID) (string, error) {
	return p[projectID], nil
}

// The filer says what a complaint is about; AppTown names the adopted
// packs of a way-of-working complaint. A complaint filed in an eval's copy
// belongs to the eval: only one about AppTown reaches AppTown's queue,
// marked with its eval, while every complaint filed elsewhere does. The copy
// record decides, so this holds after the copy project is gone.
func TestTheForgeQueueTakesOnlyAnEvalsComplaintsAboutTheForge(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	project := func(slug string) ids.ID {
		id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
			return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: slug, Name: slug})
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	real, copy := project("real"), project("copy")
	// sql-guarantee: the evals owner writes the copy record and imports this package; its operation is out of this test's reach.
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO eval_copies (project_id, eval_id, arm_id, arm) VALUES ($1, '00000000-0009-7000-8000-000000000000', '00000000-0001-7000-8000-000000000000', 'variant')`, copy); err != nil {
		t.Fatal(err)
	}
	box := NewBox(db, packsFor{real: "go-small@4", copy: "go-small@4"}, nil)
	if _, err := box.File(ctx, Entry{ProjectID: real, Text: "x", About: "the model"}); !errors.Is(err, ErrAbout) {
		t.Fatalf("an about outside the three is refused: %v", err)
	}
	filed := map[string]ids.ID{}
	for _, f := range []struct {
		key     string
		project ids.ID
		about   string
	}{{"real-wow", real, AboutWayOfWorking}, {"real-default", real, ""}, {"copy-forge", copy, AboutForge}, {"copy-wow", copy, AboutWayOfWorking}, {"copy-project", copy, AboutProject}} {
		id, err := box.File(ctx, Entry{ProjectID: f.project, Source: "run", Text: f.key, About: f.about})
		if err != nil {
			t.Fatal(err)
		}
		filed[f.key] = id
	}
	// sql-guarantee: deleting the copy's project row as project deletion does; the project owner's removal needs every other owner's participants.
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, copy); err != nil {
		t.Fatal(err)
	}
	queue, err := box.All(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	got := map[ids.ID]Complaint{}
	for _, c := range queue {
		got[c.ID] = c
	}
	if len(got) != 3 {
		t.Fatalf("queue: %+v", queue)
	}
	if c := got[filed["real-wow"]]; c.About != AboutWayOfWorking || c.PackVersion != "go-small@4" || !c.EvalID.IsZero() {
		t.Fatalf("a way-of-working complaint outside evals, with its packs: %+v", c)
	}
	if c := got[filed["real-default"]]; c.About != AboutForge {
		t.Fatalf("about defaults to AppTown: %+v", c)
	}
	if c := got[filed["copy-forge"]]; c.EvalID != idstest.N(9) || c.About != AboutForge {
		t.Fatalf("an eval's AppTown complaint reaches the queue marked with its eval: %+v", c)
	}
}

// The box keeps time by AppTown's clock: filing, archiving and the
// retention sweep all read it, however far it is from the machine's.
func TestTheBoxKeepsTimeByTheForgesClock(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	var at atomic.Int64
	box := NewBox(db, nil, func() time.Time { return time.Unix(at.Load(), 0) })
	filed := time.Now().Add(400 * 24 * time.Hour).Truncate(time.Second).UTC()
	at.Store(filed.Unix())
	if _, err := box.File(t.Context(), Entry{Source: "human", Actor: "bls", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	all, err := box.All(context.Background(), 10)
	if err != nil || len(all) != 1 || all[0].At != filed.Format(time.RFC3339) {
		t.Fatalf("filed %+v %v, want it at AppTown's %v", all, err, filed)
	}
	archived := filed.Add(Retention - time.Hour)
	at.Store(archived.Unix())
	if n, err := box.Expire(context.Background()); err != nil || n != 0 {
		t.Fatalf("expired %d within the retention: %v", n, err)
	}
	if n, err := box.ArchiveOlder(context.Background(), 24*time.Hour, "ops"); err != nil || n != 1 {
		t.Fatalf("archived %d: %v", n, err)
	}
	if all, _ = box.All(context.Background(), 10); !all[0].ArchivedAt.Time.Equal(archived) {
		t.Fatalf("archived at %v, want %v", all[0].ArchivedAt, archived)
	}
	at.Store(filed.Add(Retention + time.Hour).Unix())
	if n, err := box.Expire(context.Background()); err != nil || n != 1 {
		t.Fatalf("expired %d past the retention: %v", n, err)
	}
}

// Complaints are numbered within AppTown as people name them
// ("complaint 9"): filed at once, each still takes the next number, none
// repeated.
func TestComplaintsAreNumberedInTheOrderTheyAreFiled(t *testing.T) {
	t.Parallel()
	box := NewBox(testDB(t), nil, nil)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := box.File(context.Background(), Entry{Text: "slow", Source: "human"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	all, err := box.All(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, c := range all {
		seen[c.Number] = true
	}
	for n := int64(1); n <= 6; n++ {
		if !seen[n] {
			t.Fatalf("numbers %v lack %d", seen, n)
		}
	}
}

// A complaint's number is never given again: deleting the newest, or every
// complaint, leaves the next filing a new number, and the old number names
// nothing.
func TestADeletedComplaintsNumberIsNotGivenAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	box := NewBox(testDB(t), nil, nil)
	first, err := box.File(ctx, Entry{Text: "one", Source: "human"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.File(ctx, Entry{Text: "two", Source: "human"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []ids.ID{second, first} {
		if err := box.Delete(ctx, id, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := box.IDByNumber(ctx, 2); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("a deleted complaint's number still names something: %v", err)
	}
	third, err := box.File(ctx, Entry{Text: "three", Source: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := box.IDByNumber(ctx, 3); err != nil || id != third {
		t.Fatalf("the next filing is complaint 3: %s %v", id, err)
	}
}
