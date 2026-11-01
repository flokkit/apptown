package deployment

// Own external tests exercise private behavior without expanding production APIs.
const AlreadyApplied = alreadyApplied
const AttemptSuperseded = attemptSuperseded
const Uncertain = outcomeUncertain

type Contradiction = contradiction
type Outcome = deploymentOutcome
type Recoverer = recoverer
type RecoveryTarget = recoveryTarget
type Snapshot = snapshot

var ErrAttemptAbsent = errAttemptAbsent
var ErrReadinessTransition = errReadinessTransition
