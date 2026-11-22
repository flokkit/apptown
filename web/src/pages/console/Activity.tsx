import { waitingTone } from '../../Status'
import type { WaitingOn } from '../../wire.gen'

// The chat's one status: a dot and a word. The word is what the helper is
// doing (working, checking and landing, your turn, starting, connection
// lost), or, when that is not the reader's to know, where the change
// stands. In a change chat the dot's colour is the change's state, the
// change's own words are its title, and it pulses while the helper works;
// it is hollow when the conversation's state is not known, which is not the
// same as your turn.
export type ActivityWord = 'working' | 'checking and landing' | 'your turn' | 'starting' | 'connecting' | 'connection lost'

export function Activity({ word, change }: { word: ActivityWord | null; change?: WaitingOn }) {
  const busy = word === 'working' || word === 'checking and landing'
  const unknown = word === 'starting' || word === 'connecting' || word === 'connection lost'
  const text = word ?? change?.says ?? ''
  const dot = unknown ? 'unknown' : change ? waitingTone(change.tone) || 'neutral' : busy ? 'running' : 'neutral'
  const about = change && word ? `${text} · the change: ${change.says}` : text
  return <span className={`activity${busy ? ' busy' : ''}${unknown ? ' unknown' : ''}`} role="status" aria-label={about} title={about}>
    <span className={`activity-dot dot-${dot}`} aria-hidden="true" />{text}
  </span>
}
