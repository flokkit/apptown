package scm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm/internal/scmsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// MainVia names the door that moved a project's main.
type MainVia string

const (
	ViaLanding        MainVia = "landing"
	ViaInit           MainVia = "init"
	ViaSeed           MainVia = "seed"
	ViaCopy           MainVia = "copy"
	ViaGenesis        MainVia = "genesis"
	ViaIntegrityAdopt MainVia = "integrity-adopt"
	viaBackfill       MainVia = "backfill"
	viaUpgrade        MainVia = "upgrade" // apptown upgrade's rename commit
)

// errMainChanged: the recorded main changed between a move's preparation and
// its settlement. The caller's whole settlement rolls back.
var errMainChanged = errors.New("the recorded main changed while this move was settled; retry")

// RecordedMain is where a project's main stood when AppTown last moved it.
type RecordedMain struct {
	Branch     string    `json:"branch"`
	SHA        string    `json:"sha"`
	Via        string    `json:"via"`
	RunID      ids.ID    `json:"runId,omitzero"`
	RecordedAt time.Time `json:"recordedAt"`
}

// ReadMain returns a project's recorded main; ok is false when it is unknown.
func ReadMain(ctx context.Context, q database.Queryer, projectID ids.ID) (RecordedMain, bool, error) {
	row, err := scmsql.New(q.SQL()).RecordedMain(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return RecordedMain{}, false, nil
	}
	if err != nil {
		return RecordedMain{}, false, err
	}
	return RecordedMain{Branch: row.Branch, SHA: row.Sha, Via: row.Via, RunID: row.RunID, RecordedAt: row.RecordedAt}, true, nil
}

// mainMove is a hub's main read back after a move, ready to be recorded in
// the transaction that settles the move. Prepare it while holding the
// project's repository lock, after the ref moved; it never takes the lock.
// The record only goes forward: a main that does not descend from the
// recorded commit, or that cannot be read, is not recorded, and the old
// record (or unknown) stays.
type mainMove struct {
	projectID             ids.ID
	branch, sha, previous string
	known, record         bool
}

// PrepareMainMove reads the hub's main and the current record. It runs Git, so
// it is called before the caller's transaction opens.
func PrepareMainMove(ctx context.Context, q database.Queryer, hub string, projectID ids.ID, branch string) (mainMove, error) {
	if projectID.IsZero() || hub == "" || branch == "" {
		return mainMove{}, fmt.Errorf("recording main requires its project, hub and branch")
	}
	m := mainMove{projectID: projectID, branch: branch, sha: gitexec.HubRef(ctx, hub, "refs/heads/"+branch)}
	if err := ctx.Err(); err != nil {
		return mainMove{}, err
	}
	recorded, known, err := ReadMain(ctx, q, projectID)
	if err != nil {
		return mainMove{}, err
	}
	m.previous, m.known = recorded.SHA, known
	switch {
	case m.sha == "":
		// Main cannot be read: nothing is recorded that was not read.
	case !known:
		m.record = true
	case recorded.SHA == m.sha:
		// Already recorded: the record keeps the door that moved main there.
	default:
		m.record = isAncestor(ctx, hub, recorded.SHA, m.sha)
	}
	return m, nil
}

// RecordIn records the prepared main in the caller's transaction, compared and
// set against the record read at preparation.
// at is AppTown's time of the move.
func (m mainMove) RecordIn(ctx context.Context, tx *database.Tx, via MainVia, runID ids.ID, at time.Time) error {
	if !m.record {
		return nil
	}
	q := scmsql.New(tx.SQL())
	var n int64
	var err error
	if m.known {
		n, err = q.AdvanceMain(ctx, scmsql.AdvanceMainParams{At: at, ProjectID: m.projectID, Branch: m.branch, Sha: m.sha, Via: string(via), RunID: runID, Previous: m.previous})
	} else {
		n, err = q.InsertMain(ctx, scmsql.InsertMainParams{At: at, ProjectID: m.projectID, Branch: m.branch, Sha: m.sha, Via: string(via), RunID: runID})
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return errMainChanged
	}
	return nil
}

// RecordMain records a hub's main in its own transaction, for a door other
// than a landing whose caller holds the project's repository lock and has
// already moved main. A failure leaves the older record (or unknown).
func RecordMain(ctx context.Context, db *database.DB, hub string, projectID ids.ID, branch string, via MainVia, at time.Time) error {
	move, err := PrepareMainMove(ctx, db, hub, projectID, branch)
	if err != nil {
		return err
	}
	return db.InTx(ctx, func(tx *database.Tx) error { return move.RecordIn(ctx, tx, via, ids.Nil, at) })
}

func isAncestor(ctx context.Context, hub, older, newer string) bool {
	_, err := gitexec.HubRun(ctx, hub, "merge-base", "--is-ancestor", "--end-of-options", older, newer)
	return err == nil
}

func countBetween(ctx context.Context, hub, from, to string) (int, bool) {
	out, err := gitexec.HubRun(ctx, hub, "rev-list", "--count", from+".."+to, "--")
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	return n, err == nil
}

// MainStanding compares the recorded main with the hub, as a note for people.
// It says what differs, never who moved main: an AppTown move whose record has
// not settled reads the same as a commit made outside AppTown.
type MainStanding struct {
	// State: "recorded" (the hub is at the record), "ahead", "rewritten",
	// "unknown" (no record), or "unreadable" (the hub's main cannot be read).
	State    string        `json:"state"`
	Recorded *RecordedMain `json:"recorded,omitempty"`
	Hub      string        `json:"hub,omitempty"`
	Ahead    int           `json:"ahead,omitempty"`
	Note     string        `json:"note,omitempty"`
	Upstream *UpstreamMain `json:"upstream,omitempty"`
}

// UpstreamMain compares the record with the last fetched upstream main (the
// hub's tracking ref). It is not a fresh reading of the upstream.
type UpstreamMain struct {
	Known  bool   `json:"known"`
	Ahead  int    `json:"ahead,omitempty"`  // upstream commits the record does not have
	Behind int    `json:"behind,omitempty"` // recorded commits upstream does not have
	Note   string `json:"note,omitempty"`
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// MainStanding is ReadMainStanding against the branches' database: the
// project view's reading of where main stands.
func (p *Branches) MainStanding(ctx context.Context, hub string, projectID ids.ID, branch string) (MainStanding, error) {
	return readMainStanding(ctx, p.db, hub, projectID, branch)
}

// readMainStanding reads the record and the hub, without the repository lock.
func readMainStanding(ctx context.Context, q database.Queryer, hub string, projectID ids.ID, branch string) (MainStanding, error) {
	recorded, known, err := ReadMain(ctx, q, projectID)
	if err != nil {
		return MainStanding{}, err
	}
	s := MainStanding{Hub: gitexec.HubRef(ctx, hub, "refs/heads/"+branch)}
	if known {
		s.Recorded = &recorded
	}
	switch {
	case s.Hub == "":
		s.State, s.Note = "unreadable", "could not read main"
	case !known:
		s.State, s.Note = "unknown", "AppTown has no record of where main stands"
	case recorded.SHA == s.Hub:
		s.State = "recorded"
	case isAncestor(ctx, hub, recorded.SHA, s.Hub):
		s.State = "ahead"
		if n, ok := countBetween(ctx, hub, recorded.SHA, s.Hub); ok {
			s.Ahead = n
			s.Note = fmt.Sprintf("main is %s ahead of AppTown's record at %s", commits(n), strs.ShortSHA(recorded.SHA))
		} else {
			s.Note = fmt.Sprintf("main is ahead of AppTown's record at %s", strs.ShortSHA(recorded.SHA))
		}
	default:
		s.State = "rewritten"
		s.Note = fmt.Sprintf("the recorded %s is no longer in main's history", strs.ShortSHA(recorded.SHA))
	}
	if _, _, attached := Origin(ctx, hub); attached && known {
		s.Upstream = upstreamStanding(ctx, hub, branch, recorded.SHA)
	}
	return s, nil
}

func upstreamStanding(ctx context.Context, hub, branch, recorded string) *UpstreamMain {
	tracking := gitexec.HubRef(ctx, hub, "refs/remotes/origin/"+branch)
	if tracking == "" {
		return &UpstreamMain{Note: "last fetched upstream: unknown"}
	}
	ahead, okA := countBetween(ctx, hub, recorded, tracking)
	behind, okB := countBetween(ctx, hub, tracking, recorded)
	if !okA || !okB {
		return &UpstreamMain{Note: "last fetched upstream: unknown"}
	}
	u := &UpstreamMain{Known: true, Ahead: ahead, Behind: behind}
	var parts []string
	if ahead > 0 {
		parts = append(parts, "has "+commits(ahead)+" AppTown's record does not")
	}
	if behind > 0 {
		parts = append(parts, "lacks "+commits(behind)+" of AppTown's record")
	}
	if len(parts) > 0 {
		u.Note = "last fetched upstream " + strings.Join(parts, " and ")
	}
	return u
}
