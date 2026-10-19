# Carrying accepted Work into a landing

Accepting a change can count as approving its landing. Whether it does is
decided in one place, `landings.carry` in `internal/forge/workflow`, for each
approval step of the landing in turn.

## When the acceptance counts

For an approval step waiting in the landing's current stage, the acceptance
counts only if all of these hold:

- **The accepter was shown its sentence.** A change's waiting acceptance
  shows what the landing's approvals would ask, read from the landing the
  project's way of working pins (`workflow.LandingMeans`), and every client
  (the web's decision form, `forge approve`, `forge loop`) sends back what it
  displayed. The acceptance vote records those sentences as `alsoApproves`
  only if the landing still asks exactly them; if the landing's words changed
  between display and decision the approval is refused (`409`, code
  `question-changed`) so the person reads the new words. A client that sends
  nothing records nothing, and its acceptance does not count for the landing. A landing approval whose
  `means` is not among them gets no carried vote: a yes counts only for words
  the person read when they gave it.
- **The accepter was shown the same list of what the change adds.** An
  approval of a commit shows what that commit adds to the app
  (`workflow.ChangeAdds`, read by `internal/forge/changeadds` from the app's
  declarations). Every
  approving vote sends back the digest of the list it showed, and the vote
  records the list with the commit; a missing digest is refused
  (`adds-not-shown`), a stale one is refused as `question-changed`. The
  acceptance counts only if the landing reads the same rows for its
  candidate; an acceptance recorded without a list never counts. The
  carried vote records the accepter's list.
- **The landing's checks brought no news.** The yes was given before the
  landing's checks ran, so it stands only if they said nothing the accepter
  could not have known (`checkNews`): no step overridden, no step failed,
  errored or waiting on a person, no check errors, no check that could not
  judge the change. What is true of every landing is not news: a scan point
  with no scanner attached, a runtime check the forge itself could not run
  (stock land starts no preview), a model check with no model bound. Nor is
  what already fails on main, preexisting or undecided: it is the estate's,
  not the change's doing, and the change's reviewer judged the undecided
  ones from its diff. What the change introduced fails the gate.
- **The accepter could approve the step themselves.** The same checks a
  manual approval passes (`commandAuthority.requireDecision`): an enabled
  account, an eligible approver for the step's `approvers` (or the project's
  default approvers), and the approve capability. The authority is asked
  about the accepter and records its decision; no synthetic principal acts.

Where all hold, the accepter's vote is cast with the comment "accepted at the
change gate (run N)" and a `landing.acceptance.carried` receipt. Otherwise
the step waits for a person and a `landing.acceptance.withheld` receipt says
why. A completed working run with no named human acceptance (`forge-system`)
carries nothing. The acceptance is the approval in the last stage of a
change's working run; earlier approvals, such as a plan's, never count for a
landing. Under a quorum a carried vote is one vote.

## When it is judged

The engine calls `carry` whenever it advances a run, before reading the gate,
and again when a stage opens with nothing automatic to run. For a run that is
not an accepted landing that costs one definition read. For a landing,
`carry` judges only once every other step of the current stage has answered,
so an approval that shares a stage with checks is judged against their
results, and stock land's separate approval stage is judged as it opens. Each
approval is judged once: the carried or withheld receipt names the step, and
a later advance skips it. If a carried vote leaves the stage nothing else to
wait for, the engine advances again.

Eligibility is asked before the vote's transaction, because it reads policy
on other connections that must not wait behind the vote's locks; as with a
manual approval, it is checked just before the write, not inside it. Under
the project and run locks, which every step decision also takes, the vote
transaction rechecks that the run has not moved, that no concurrent advance
judged the step first, and the run's own facts again: whether the stage's
checks have all answered, and whether they brought news. An override or a
check that settled while the acceptance was being judged is seen there. A failure
leaves the approval waiting for a person, which is the safe direction, and a
later advance tries again.

## The handoff

`landings.apply` records the handoff from accepted Work to its landing and
casts no votes. Under the project/run locks it checks the completed root
Work, the selected landing, shared Change ownership and the absence of a
superseding continuation, then commits together:

- The `acceptance_applied` marker.
- The paired `landing.autostarted` receipts, naming the accepter, and any
  adoption-change receipt.

A failed or suppressed required write rolls back the whole handoff.
Concurrent retries reuse one handoff without duplicating receipts. After
commit, the coordinator prepares the run workspace and asks the engine to
advance that exact snapshot, which is when the first stage's approvals are
judged. The durable completion job owns retry scheduling. On retry,
the handoff can advance only its recorded stage/revision, never a later stage
or new revision. A superseded or terminal run is not resumed. A handoff whose
receipt lacks that scope is never given one inferred from the current run; an
operator advances the run explicitly after inspecting it.
