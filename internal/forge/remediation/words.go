package remediation

// Remediation as a queue (design/subsystems/remediation.md): a job over the estate that
// fixes findings ONE governed Change per project at a time, worked by a
// declared pool of remediation workers, verified by a review, with "stuck" as
// a named state a person sees. This file is the model: the pool kinds, the
// job and its items, and the words a person reads. The doors are in
// service.go, the loop that moves items in worker.go, and fixes as records
// in the App's conformance_fixes.go.
//
// Attended only: every approval is a person's. Items
// stop with a reason instead of retrying; a provider rate limit is the one
// transient the queue backs off from on its own.

import (
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/workflow"

	"flokkit.io/forge/internal/stamp"
)

// Worker pool kinds. Other kinds (review workers, import workers) come later;
// the pool is keyed by kind from the start so they can.
const WorkerKindRemediation = "remediation"

const DefaultWorkers = 2

// Remediation statuses (design §12.10).
const (
	RemRunning            = "running"
	RemPaused             = "paused"
	RemCompleted          = "completed"
	RemCompletedWithStuck = "completed_with_stuck"
	RemCancelled          = "cancelled"
)

// Item states (design §4).
const (
	ItemQueued    = "queued"
	ItemWorking   = "working"
	ItemAccepting = "accepting"
	ItemLanding   = "landing"
	ItemVerifying = "verifying"
	ItemDone      = "done"
	ItemStuck     = "stuck"
	ItemSkipped   = "skipped"
)

// Remediation is one job.
type Remediation struct {
	ID       ids.ID `json:"id"`
	Number   int64  `json:"number"`   // within the forge, as people name it ("remediation 9")
	Scope    string `json:"scope"`    // all | check | project | review
	ScopeRef string `json:"scopeRef"` // the check key, project slug, or review id
	Mode     string `json:"mode"`     // attended: every approval is a person's
	Actor    string `json:"actor"`
	// ActorPrincipal is the starter's principal; zero when not resolved to a person.
	ActorPrincipal ids.ID     `json:"actorPrincipal,omitzero"`
	Status         string     `json:"status"`
	Concurrency    int        `json:"concurrency"` // 0 = the pool's size
	Retries        int        `json:"retries"`
	ReviewID       ids.ID     `json:"reviewId"`
	CreatedAt      string     `json:"createdAt"`
	UpdatedAt      string     `json:"updatedAt"`
	FinishedAt     stamp.Time `json:"finishedAt,omitzero"`
	// Counts, filled by readers.
	Counts   map[string]int `json:"counts,omitempty"`
	Sentence string         `json:"sentence,omitempty"`
	Cost     float64        `json:"cost,omitempty"`
}

// Item is one project's fix inside a job.
type Item struct {
	ID            ids.ID `json:"id"`
	RemediationID ids.ID `json:"remediationId"`
	ProjectID     ids.ID `json:"projectId"`
	ProjectSlug   string `json:"projectSlug"`
	CheckKey      string `json:"checkKey"`
	Block         string `json:"block"`
	Version       int    `json:"version"`
	Check         string `json:"check"`
	Rank          int    `json:"rank"`
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
	// Frozen provenance (design §12.1): the exact finding this item fixes.
	OriginVerdictID    ids.ID `json:"originVerdictId"`
	OriginSHA          string `json:"originSha"`
	OriginEvaluatorVer string `json:"originEvaluatorVer,omitempty"`
	OriginEvidenceHash string `json:"originEvidenceHash,omitempty"`
	OriginDetail       string `json:"originDetail,omitempty"`
	Attempt            int    `json:"attempt"`
	TransientRetries   int    `json:"transientRetries"`
	ChangeID           ids.ID `json:"changeId,omitzero"`
	RunID              ids.ID `json:"runId,omitzero"`
	// ChangeNumber and RunNumber name ChangeID and RunID within the project;
	// filled on the page's read, 0 when none or elsewhere.
	ChangeNumber     int64      `json:"changeNumber"`
	RunNumber        int64      `json:"runNumber"`
	LeaseWorker      string     `json:"leaseWorker,omitempty"`
	LeaseUntil       stamp.Time `json:"leaseUntil,omitzero"`
	NotBefore        stamp.Time `json:"notBefore,omitzero"`
	VerifiedReviewID ids.ID     `json:"verifiedReviewId,omitzero"`
	CostMicros       int64      `json:"costMicros"`
	OutcomeRevision  int64      `json:"-"`
	UpdatedAt        string     `json:"updatedAt"`
	// Doing: what the item's run is on right now (filled on read, not stored):
	// "build-app (agent)", "boot", "review-build (agent)", "accept"…
	Doing string `json:"doing,omitempty"`
}

// Remediation keeps its display words; Work owns Step selection and state.
func activityDescription(a workflow.PendingStep) string {
	switch {
	case a.Type == "agent/pi" || a.Type == "agent/review":
		if a.State == workflow.StatusRunning {
			return a.Name + " (agent)"
		}
		return a.Name + " (agent, queued)"
	case a.Type == "approval":
		return a.Name + " (needs a person)"
	default:
		return a.Name
	}
}

// Attempt is one Change tried for an item.
type Attempt struct {
	ID         ids.ID     `json:"id"`
	ItemID     ids.ID     `json:"itemId"`
	N          int        `json:"n"`
	ChangeID   ids.ID     `json:"changeId"`
	RunID      ids.ID     `json:"runId"`
	BaseSHA    string     `json:"baseSha"`
	Outcome    string     `json:"outcome"`
	CostMicros int64      `json:"costMicros"`
	Reason     string     `json:"reason,omitempty"`
	StartedAt  string     `json:"startedAt"`
	FinishedAt stamp.Time `json:"finishedAt,omitzero"`
}

// --- scope resolution -------------------------------------------------------------

// severityRankOf orders items worst first within a project.
func severityRankOf(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}

// progressSentence describes the summary without changing its recorded counts.
func (m Remediation) progressSentence() string {
	sentence := fmt.Sprintf("%d of %d done", m.Counts[ItemDone], m.Counts["total"])
	inflight := m.Counts[ItemWorking] + m.Counts[ItemAccepting] + m.Counts[ItemLanding] + m.Counts[ItemVerifying]
	for _, part := range []struct {
		n     int
		label string
	}{
		{inflight, "in progress"}, {m.Counts[ItemQueued], "queued"},
		{m.Counts[ItemStuck], "stuck"}, {m.Counts[ItemSkipped], "skipped"},
	} {
		if part.n > 0 {
			sentence += fmt.Sprintf(" · %d %s", part.n, part.label)
		}
	}
	return sentence
}

// scopeSentence says what a job covers, in words.
func (m Remediation) scopeSentence() string {
	switch m.Scope {
	case "check":
		if i := strings.Index(m.ScopeRef, "·"); i >= 0 {
			return "fix " + m.ScopeRef[i+len("·"):] + " everywhere"
		}
		return "fix " + m.ScopeRef + " everywhere"
	case "project":
		return "fix everything on " + m.ScopeRef
	case "review":
		return "fix what review #" + m.ScopeRef + " found"
	default:
		return "fix all findings"
	}
}

// Occupancy is what the Remediations tab shows at the top: the pool, who
// holds it, and whether the queue is backing off from a provider.
type Occupancy struct {
	Kind          string `json:"kind"`
	Workers       int    `json:"workers"`
	Busy          int    `json:"busy"`
	Holding       []Item `json:"holding"`
	Queued        int    `json:"queued"`
	CooldownUntil string `json:"cooldownUntil,omitempty"`
	CooldownWhy   string `json:"cooldownWhy,omitempty"`
}
