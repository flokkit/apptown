import type { ChangeState, Doing, RunStatus } from './api'

// One place that says what colour a state is (design/subsystems/ui-coherence.md
// § Colour: one colour, one job). Returns the chip tone class — pass (green:
// it shipped / it's live), pending (amber: a person is needed), running
// (cyan: the machine is working), fail (red: stuck / failed), or '' for
// neutral. Replaces the three chip maps that used to disagree.
const TONE: Record<ChangeState | RunStatus | Doing, string> = {
  // change states
  proposed: '', in_progress: 'running', landing: 'pending', shipped: 'pass', abandoned: '',
  // run statuses
  running: 'running', done: 'pass',
  // what a running run is doing
  'waiting on you': 'pending', blocked: 'fail', 'agent working': 'running', working: 'running',
  queued: '', 'ready to advance': 'pass',
}

export function chipFor(state: ChangeState | RunStatus | Doing | '' | undefined): string {
  return state ? TONE[state] ?? '' : ''
}

// The chip's words for a change state: the server's snake_case, spoken,
// in the glossary's words. A closed change whose work landed (merged or
// released) is shipped; one closed on an accepted record with nothing to
// land (research, a spec) is finished. landed is passed where the reader
// knows it; without it a closed change reads shipped, its state's name.
// An abandoned change AppTown closed because its run found nothing to
// change (closedAs, recorded by the engine) is closed, not abandoned.
export function changeStateLabel(state: ChangeState, landed?: boolean, closedAs?: string): string {
  // A change whose own landing merged but could not ship, settled when a
  // later landing shipped main containing its merge (closedAs, recorded by
  // the engine): its source shipped, its own deploy did not pass.
  if (state === 'shipped' && closedAs === 'shipped-with') return 'shipped with a later change'
  if (state === 'shipped') return landed === false ? 'finished' : 'shipped'
  if (state === 'abandoned' && closedNothingToChange(closedAs)) return 'closed: nothing to change'
  return state.replace('_', ' ')
}

// closedNothingToChange: the change's recorded closedAs says AppTown
// closed it because there was nothing to change. Read only from that fact,
// never from the reason's words, which a person can also type.
export function closedNothingToChange(closedAs: string | undefined): boolean {
  return closedAs === 'nothing-to-change'
}
