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
