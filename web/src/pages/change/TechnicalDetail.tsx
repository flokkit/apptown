import { ReactNode, useSyncExternalStore } from 'react'
import { useGet } from '../../data'
import type { AuthMe } from '../../wire.gen'

const choices = new Map<string, boolean>()
const listeners = new Set<() => void>()
const subscribe = (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn) } }
export function useTechnicalDetail(): [boolean, (shown: boolean) => void] {
  const me = useGet<AuthMe>('/auth/me').data
  const key = me?.user ? `apptown.technical-detail.${encodeURIComponent(me.user)}` : ''
  const snapshot = () => {
    if (!choices.has(key)) {
      let shown = false
      try { shown = !!key && localStorage.getItem(key) === '1' } catch { /* Storage is optional. */ }
      choices.set(key, shown)
    }
    return choices.get(key)!
  }
  const shown = useSyncExternalStore(subscribe, snapshot, () => false)
  return [shown, value => {
    choices.set(key, value)
    try { if (key) localStorage.setItem(key, value ? '1' : '0') } catch { /* Keep the choice for this session. */ }
    listeners.forEach(fn => fn())
  }]
}
// Technical detail (names, versions, raw records) is one per-person switch,
// kept in the account menu; a faint 🕶️ beside the name says it is on.
export function TechnicalDetailSwitch() {
  const [shown, setShown] = useTechnicalDetail()
  return <button type="button" className={`subtle ch-goggles${shown ? ' active' : ''}`} aria-pressed={shown} aria-label="Techno Goggles" title="Techno Goggles" onClick={() => setShown(!shown)}><span aria-hidden="true">🕶️</span></button>
}
export function TechnicalDetailMark() {
  const [shown] = useTechnicalDetail()
  return shown ? <span className="goggles-mark" title="Techno Goggles are on" aria-label="Techno Goggles are on">🕶️</span> : null
}

// Hidden words retain their space, so toggling detail never rearranges the page.
export function TechnicalWords({ shown, children }: { shown: boolean; children: ReactNode }) {
  // Hidden, it takes no room: an empty line is not left where it would be.
  return <span className="ch-technical small muted" aria-hidden={!shown} hidden={!shown}>{children}</span>
}
