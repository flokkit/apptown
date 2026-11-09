import { Status } from './Status'
import { Link } from 'react-router-dom'
import { DatabaseBackup, Plug, Server, ShieldCheck } from 'lucide-react'
import { useHolds } from './instancePages'
import { useGet, type Read } from './data'
import { StaleNote } from './ReadGate'
import type { AttentionView, AttentionItem, AuthMe } from './wire.gen'

export function useAttention() {
  const me = useGet<AuthMe>('/auth/me').data
  const signedIn = !!me && (me.mode !== 'internal' || !!me.user)
  return useGet<AttentionView>(signedIn ? '/attention' : null, { poll: 30000 })
}

// These are exactly the rows the Attention page renders, shared by every count.
export function attentionRows(data: AttentionView) {
  return { items: data.items, sources: data.sources.filter(s => s.unknown && !data.items.some(i => i.unknown && (i.key === s.key || i.key.startsWith(s.key + '/')))) }
}

export function attentionState(q: Pick<Read<AttentionView>, 'data' | 'refreshFailed' | 'paused'>) {
  const rows = q.data ? attentionRows(q.data) : null
  return {
    count: rows ? rows.items.filter(i => !i.unknown).length : null,
    unknown: rows ? rows.sources.length + rows.items.filter(i => i.unknown).length : 0,
    known: !!rows && !q.refreshFailed && !q.paused && !rows.sources.length && rows.items.every(i => !i.unknown),
  }
}

export function attentionSummary(count: number | null, unknown: number): string {
  if (count === null) return 'Status not known'
  return [count > 0 ? `${count} to fix` : '', unknown > 0 ? `${unknown} not known` : ''].filter(Boolean).join(' · ') || 'All quiet'
}

// Release approval is a person's pending decision, identified by its source key.
export const attentionItemWord = (item: AttentionItem) => item.statusLabel

// The four kinds of condition Attention groups, each with the icon its area
// uses in the menu. A key's prefix says which kind it is.
export const ATTENTION_GROUPS = [
  { key: 'hosts', title: 'Hosts and deployments', icon: Server },
  { key: 'backups', title: 'Backups', icon: DatabaseBackup },
  { key: 'integrations', title: 'Integrations', icon: Plug },
  { key: 'checks', title: 'App checks', icon: ShieldCheck },
] as const
export type AttentionGroup = typeof ATTENTION_GROUPS[number]['key']
export const attentionGroup = (key: string): AttentionGroup =>
  key.startsWith('protection/') ? 'backups' : key.startsWith('integrations') ? 'integrations' : key.startsWith('checks') ? 'checks' : 'hosts'

// useOperator: whether the reader can fix the conditions Attention lists
// (deployment targets or connections); others are not shown them in the
// top bar or on Home.
export function useOperator() {
  const targets = useHolds('platform:manage-deploy-targets')
  const connectors = useHolds('integration:manage-connectors')
  return targets || connectors
}

// AttentionLights: one small light per kind of condition, lit only while
// something of that kind is confirmed broken, for the people who can fix
// it. Unlit kinds stay in place in grey, so a lit one stands out by colour
// and position. A check that could not run lights nothing (Attention lists
// it), and members never see admin conditions. On a phone (compact) the
// four fold into one dot, coloured by the worst of them, that opens the
// list: four lights side by side are too small to tap there.
export function AttentionLights({ compact = false }: { compact?: boolean }) {
  const q = useAttention()
  const operator = useOperator()
  if (!operator) return null
  // A list that could not be read is not a list with nothing on it.
  if (!q.data) return q.err ? <Link className="small attention-unread" to="/attention" title={`Attention could not be read: ${q.reason || q.err}`}><Status word="unknown" /></Link> : null
  const groups = ATTENTION_GROUPS.map((g) => ({ ...g, items: q.data!.items.filter((i) => !i.unknown && attentionGroup(i.key) === g.key) }))
  const total = groups.reduce((n, g) => n + g.items.length, 0)
  const state = attentionState(q)
  const label = total > 0 ? `${total} to fix` : attentionSummary(state.count, state.unknown)
  // Low disk and pending approvals are amber, like their Attention rows.
  const shade = (items: AttentionItem[]) => items.length === 0 ? ' off'
    : items.every((i) => i.statusLabel === 'low' || attentionItemWord(i) === 'waiting for approval') ? ' low' : ''
  if (compact) return <span className="row small attention-lights">
    <Link className={`attention-dot${shade(groups.flatMap((g) => g.items))}`} to="/attention" title={label} aria-label={label}><span aria-hidden /></Link>
    <StaleNote q={q} inline />
  </span>
  return <span className="row small attention-lights" role="group" aria-label={label}>
    {groups.map((g) => {
      const words = g.items.length === 0 ? `${g.title}: no known issues` : `${g.title}: ${g.items.length} to fix`
      return <Link key={g.key} className={`attention-light${shade(g.items)}`} to={`/attention#${g.key}`} title={words} aria-label={words}>
        <g.icon aria-hidden size={16} />
      </Link>
    })}
    <StaleNote q={q} inline />
  </span>
}
