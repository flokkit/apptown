import { PRODUCT } from '../../brand'
import type { Event } from '../../wire.gen'

const short = (sha: unknown) => (typeof sha === 'string' ? sha.slice(0, 8) : '?')

// eventSentence says a run event in plain words where there is one to say;
// null leaves the event as recorded (its kind and payload).
export function eventSentence(e: Pick<Event, 'kind' | 'payload'>): string | null {
  const p = (e.payload ?? {}) as Record<string, unknown>
  const main = typeof p.mainBranch === 'string' ? p.mainBranch : 'main'
  switch (e.kind) {
    case 'main.merged':
      return `${PRODUCT} merged ${main} (${short(p.main)}) into this change before step ${p.step}, as commit ${short(p.sha)}.`
    case 'main.conflict': {
      const files = Array.isArray(p.conflicts) ? p.conflicts.join(', ') : 'some files'
      return `${PRODUCT} could not merge ${main} (${short(p.main)}) into this change before step ${p.step}: ${files} conflict. Catch up in the change's chat.`
    }
  }
  return null
}

// Unknown event kinds still have their exact record one tap away.
export function eventTitle(kind: string): string {
  const titles: Record<string, string> = {
    'run.created': 'Work started', 'run.finished': 'Work finished', 'run.done': 'Work finished',
    'run.abandoned': 'Work stopped', 'step.passed': 'Check passed', 'step.failed': 'Check failed',
    'step.started': 'Step started', 'step.finished': 'Step finished', 'approval.decided': 'Approval recorded',
    'main.merged': 'Updated from main', 'main.conflict': 'Needs an update from main',
    'integration.resolved': 'Connection checked', 'advance.blocked': 'Waiting for a check',
  }
  return titles[kind] || 'Activity recorded'
}
