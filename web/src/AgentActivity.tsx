import { useState } from 'react'
import { Status, STATUS_LIGHT } from './Status'

// The same chat turn and connection state, kept in the page flow so it
// cannot cover the composer or the conversation.
export default function AgentActivity({ busy, connected }: { busy: boolean; connected: boolean }) {
  const [min, setMin] = useState(() => localStorage.getItem('robot-min') === '1')
  const word = !connected ? 'unknown' : busy ? 'working' : 'waiting for you'
  const caption = !connected ? 'connection lost' : busy ? 'working' : 'your turn'
  const toggle = () => { setMin(!min); localStorage.setItem('robot-min', min ? '0' : '1') }
  return <button className={'subtle agent-activity' + (min ? ' compact' : '')} onClick={toggle}
    aria-label={`${caption}: ${min ? 'show' : 'hide'} activity label`} title={caption}>
    <Status word={word} tone={STATUS_LIGHT[word]} />
  </button>
}
