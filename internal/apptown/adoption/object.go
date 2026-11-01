package adoption

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/fault"
)

var ErrPreparedRevision = errors.New("adoption was prepared from another revision")

// ErrNotAdoptable refuses a candidate whose own contents disagree, such as two
// packs pinning one workflow at different versions. The words after it name
// the problems; the pack's authors, not AppTown, can resolve them.
var ErrNotAdoptable = fault.New(fault.Unprocessable, "cannot be adopted as asked")

// ErrPackNotOffered is a new membership of a pack the instance turned off.
var ErrPackNotOffered = fault.New(fault.Conflict, "the pack is not offered")
var errCapabilityUnavailable = errors.New("required adoption capability is unavailable")

// adoptionPlan is an immutable decision prepared for one project and revision. Its
// contents are private so only Prepare can admit a candidate for advancement.
type adoptionPlan struct {
	projectID   ids.ID
	revision    int64
	receipt     Receipt
	memberships []Membership
	policies    []preparedPolicy
	extensions  []string
	kind        AdoptionKind
	reason      string
	reuse       bool
}

type preparedPolicy struct {
	name, description, rule, origin string
	config                          []byte
}

// event is the adoption decision to persist. Recorded is false when reviewing
// unchanged content settles drift without creating another historical receipt.
type event struct {
	ProjectID ids.ID
	Revision  int64
	Kind      AdoptionKind
	Reason    string
	Digest    string
	Guidance  int
	Workflows int
	Drift     string
	Recorded  bool
}

// prepare admits an exact candidate without changing current state. The
// capability callback is the one-method transaction port supplied by the service.
func (a Adoption) prepare(c Candidate, attached func([]string) error) (adoptionPlan, error) {
	if len(c.Problems) != 0 {
		return adoptionPlan{}, fmt.Errorf("%w: %s", ErrNotAdoptable, strings.Join(c.Problems, "; "))
	}
	if a.ProjectID.IsZero() || c.ProjectID != a.ProjectID {
		return adoptionPlan{}, fmt.Errorf("candidate belongs to another project")
	}
	required := slices.Clone(c.RequiredCapabilities)
	slices.Sort(required)
	required = slices.Compact(required)
	if len(required) != 0 {
		if attached == nil {
			return adoptionPlan{}, errCapabilityUnavailable
		}
		if err := attached(required); err != nil {
			return adoptionPlan{}, err
		}
	}
	p := adoptionPlan{projectID: a.ProjectID, revision: c.ExpectedRevision, receipt: cloneReceipt(c.Receipt), kind: AdoptionAdopt}
	for _, pick := range c.Packs {
		p.memberships = append(p.memberships, Membership(pick))
	}
	for _, policy := range c.Installation.Policies {
		if _, err := checkPolicy(&policy); err != nil {
			return adoptionPlan{}, err
		}
		config, err := json.Marshal(policy.With)
		if err != nil {
			return adoptionPlan{}, err
		}
		p.policies = append(p.policies, preparedPolicy{name: policy.Name, description: policy.Description, rule: policy.Rule, origin: policy.Origin, config: config})
	}
	p.extensions = slices.Clone(c.Installation.Extensions)
	return p, nil
}

func revisionMismatch(current, expected int64) error {
	return fmt.Errorf("the project is on revision %d, not %d as prepared — prepare again: %w", current, expected, ErrPreparedRevision)
}

// advance owns the expected-revision rule. Failure never mutates the receiver;
// the returned value is saved only after every transaction participant succeeds.
func (a Adoption) advance(p adoptionPlan, expected int64) (*Adoption, event, error) {
	if expected != a.Revision {
		return nil, event{}, revisionMismatch(a.Revision, expected)
	}
	if a.ProjectID.IsZero() || p.projectID != a.ProjectID || p.revision != a.Revision {
		return nil, event{}, ErrPreparedRevision
	}
	if !p.kind.standing() {
		return nil, event{}, errAdoptionKind
	}
	next := a
	next.Drift = ""
	if !p.reuse {
		if a.Revision < 0 || a.Revision == math.MaxInt64 {
			return nil, event{}, fmt.Errorf("adoption revision cannot advance from %d", a.Revision)
		}
		next.Revision++
	}
	e := event{ProjectID: a.ProjectID, Revision: next.Revision, Kind: p.kind, Reason: p.reason,
		Digest: Digest(p.receipt), Guidance: len(p.receipt.Guidance), Workflows: len(p.receipt.Workflows), Recorded: !p.reuse}
	return &next, e, nil
}

// noteDrift records an observation without changing the selected revision or
// rewriting its immutable receipt. An empty note is an explicit cleared review.
func (a *Adoption) noteDrift(note string) event {
	a.Drift = note
	return event{ProjectID: a.ProjectID, Revision: a.Revision, Drift: note}
}

func cloneReceipt(r Receipt) Receipt {
	r.Guidance = slices.Clone(r.Guidance)
	r.Workflows = slices.Clone(r.Workflows)
	r.Policies = slices.Clone(r.Policies)
	r.Extensions = slices.Clone(r.Extensions)
	r.Packs = slices.Clone(r.Packs)
	return r
}

func (a Adoption) prepareRecord(receipt Receipt, kind AdoptionKind, reason string, prior *RecordedRevision) (adoptionPlan, error) {
	if !kind.standing() {
		return adoptionPlan{}, errAdoptionKind
	}
	if err := a.requireReceipt(prior); err != nil {
		return adoptionPlan{}, err
	}
	return adoptionPlan{projectID: a.ProjectID, revision: a.Revision, receipt: cloneReceipt(receipt), kind: kind, reason: reason,
		reuse: prior != nil && prior.Digest == Digest(receipt)}, nil
}

func (a Adoption) requireReceipt(prior *RecordedRevision) error {
	if prior != nil && (prior.ProjectID != a.ProjectID || prior.Revision != a.Revision) {
		return ErrPreparedRevision
	}
	if a.Revision > 0 && prior == nil {
		return fmt.Errorf("project %d: revision %d: %w", a.ProjectID, a.Revision, ErrRevisionUnavailable)
	}
	return nil
}
