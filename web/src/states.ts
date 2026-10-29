import type { ChangeState, Doing, RunStatus } from './api'

// One place that says what colour a state is (design/subsystems/ui-coherence.md
// § Colour: one colour, one job). Returns the chip tone class — pass (green:
// it shipped / it's live), pending (amber: a person is needed), running
// (cyan: the machine is working), fail (red: stuck / failed), or '' for
// neutral. Replaces the three chip maps that used to disagree.
const TONE: Record<ChangeState | RunStatus | Doing, string> = {
  // change states
  in_progress: 'running', landing: 'pending', shipped: 'pass', abandoned: '',
  // run statuses
  running: 'running', done: 'pass',
  // what a running run is doing
  'waiting on you': 'pending', blocked: 'fail', 'agent working': 'running', working: 'running',
  queued: '', 'ready to advance': 'pass',
}

export function chipFor(state: ChangeState | RunStatus | Doing | '' | undefined): string {
  return state ? TONE[state] ?? '' : ''
}

// closedNothingToChange: the change's recorded closedAs says AppTown
// closed it because there was nothing to change. Read only from that fact,
// never from the reason's words, which a person can also type.
export function closedNothingToChange(closedAs: string | undefined): boolean {
  return closedAs === 'nothing-to-change'
}
