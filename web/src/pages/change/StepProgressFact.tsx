import type { Step } from '../../api'
import type { StepProgress, WaitingOn } from '../../wire.gen'
import { useGet } from '../../data'
import { StaleNote } from '../../ReadGate'
import { ProgressBar } from '../../ProgressBar'
import { testsNote } from './Evidence'
import { approversOf } from './next'
const checks = new Set(['scan', 'tests', 'guidance/conformance'])

export function StepProgressFact({ step: s, waitingOn }: { step: Step; waitingOn?: WaitingOn }) {
  const live = useGet<StepProgress>(s.status === 'running' ? `/steps/${s.id}/live?from=0` : null, { poll: 3000 })
  const line = live.data?.lines?.at(-1)
  const standards = s.type === 'guidance/conformance' && s.status === 'running'
  const position = standards ? line?.match(/^(?:(\d+) of (\d+)|check (\d+)) · ([^·]+) · ([^·]+?)(?: · [\d.]+s)?$/) : null
  const current = position ? Number(position[1] || position[3]) : 0
  const total = position?.[2] ? Number(position[2]) : undefined
  const completed = position?.[5] === 'checking' ? current - 1 : current
  const queued = waitingOn?.queue && s.status === 'pending' && s.id === waitingOn.step && s.runId === waitingOn.run && s.attempt === waitingOn.attempt ? waitingOn.queue.says : undefined
  const fact = queued || (s.status === 'running' && line ? line.split('\n').at(-1)!.slice(0, 180) : standards ? line || s.name.replace(/[-_]/g, ' ')
    : s.type === 'approval' ? s.evidence.approver || approversOf(s)
    : s.type === 'build' ? live.data?.lines?.at(-1)?.slice(0, 180) || s.evidence.reason || (s.status === 'running' ? 'Building the accepted work' : s.status === 'pending' ? 'Waiting to build' : s.status === 'pass' ? 'Accepted work built' : 'Build stopped')
    : s.type === 'agent/pi' ? s.status === 'running' ? 'Helper working' : s.status === 'pending' ? 'Waiting to start' : s.evidence.reason || 'Helper finished'
    : checks.has(s.type) ? s.status === 'not_applicable' ? `Not applicable: ${(s.type === 'scan' && s.evidence.notApplicable) || s.evidence.reason || 'No applicable check'}` : s.status === 'skipped' ? `Skipped: ${s.evidence.reason || 'Not run'}` : s.type === 'tests' && s.status === 'pass' ? testsNote(s.evidence).note : '' : s.evidence.reason)
  return <span className="ch-now-fact">{fact}{standards && position && <ProgressBar label="Standards check" value={completed} total={total} />}<StaleNote q={live} />{live.err && <span className="error">Progress could not be read: {live.err}</span>}</span>
}

