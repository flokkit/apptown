import { Switch } from './Switch'
import { useSyncExternalStore } from 'react'
import { useGet } from './data'
import type { AuthMe, Message } from './wire.gen'

// Keep the viewer's choice in memory too: restricted storage must not stop
// the switch working, including when several conversations are on screen.
const choices = new Map<string, boolean>()
const listeners = new Set<() => void>()
const subscribe = (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn) } }
export function useAgentActions(): [boolean, (shown: boolean) => void] {
  const me = useGet<AuthMe>('/auth/me').data
  const key = me?.user ? `apptown.show-agent-actions.${encodeURIComponent(me.user)}` : ''
  const snapshot = () => {
    if (!choices.has(key)) {
      let shown = false
      try { shown = !!key && localStorage.getItem(key) === '1' } catch { /* Storage is optional. */ }
      choices.set(key, shown)
    }
    return choices.get(key)!
  }
  const shown = useSyncExternalStore(subscribe, snapshot, () => false)
  return [shown, (value) => {
    choices.set(key, value)
    try { if (key) localStorage.setItem(key, value ? '1' : '0') } catch { /* Keep the in-memory choice. */ }
    listeners.forEach(fn => fn())
  }]
}

export function AgentActionsSwitch({ shown, onChange }: { shown: boolean; onChange: (shown: boolean) => void }) {
  return <label className="agent-actions-switch small muted">
    <Switch label="Show agent actions" checked={shown} onChange={onChange} />
    Show agent actions
  </label>
}

const TOOL_WORDS: Record<string, string> = {
  bash: 'ran', read: 'read', edit: 'edited', write: 'wrote',
  grep: 'searched', find: 'looked for', ls: 'listed',
}

// What the helper's tool use amounts to, in words: "ran 4 commands, read
// 1 file, edited 1 file". The commands themselves are one click down.
const TOOL_COUNTS: Record<string, [string, string]> = {
  bash: ['ran', 'command'], read: ['read', 'file'], edit: ['edited', 'file'], write: ['wrote', 'file'],
  grep: ['searched', 'time'], find: ['looked for files', ''], ls: ['listed', 'folder'],
}
export function toolSummary(tools: { name: string }[]): string {
  const counts = new Map<string, number>()
  for (const t of tools) counts.set(t.name, (counts.get(t.name) ?? 0) + 1)
  return [...counts].map(([name, n]) => {
    const [verb, noun] = TOOL_COUNTS[name] ?? [`used ${name}`, 'time']
    return noun ? `${verb} ${n} ${noun}${n === 1 ? '' : 's'}` : verb
  }).join(', ')
}

// The change page lets its switch reveal actions; live chat also offers individual folds.
export function ToolFold({ tools, show = false }: { tools: { name: string; detail?: string }[]; show?: boolean }) {
  return <div className="tool-fold">
    <p className="small">{toolSummary(tools)}</p>
    {show && tools.map((t, i) => <div key={i} className="small tool-line">{TOOL_WORDS[t.name] ?? t.name}{t.detail ? <> <code>{t.detail}</code></> : ''}</div>)}
  </div>
}

// A consecutive run folds without losing the original action order.
export function foldTools<T extends Message>(msgs: T[]): T[] {
  const out: T[] = []
  for (const m of msgs) {
    const prev = out[out.length - 1]
    const toolOnly = (b?: Message) => !!b && b.role === 'helper' && !b.text && !b.error && !b.stopped && (b.tools?.length ?? 0) > 0
    if (toolOnly(m) && toolOnly(prev)) {
      out[out.length - 1] = { ...prev!, tools: [...prev!.tools!, ...m.tools!] }
    } else {
      out.push(m)
    }
  }
  return out
}

