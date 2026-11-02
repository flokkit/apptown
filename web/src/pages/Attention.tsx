import { Status } from '../Status'
import { Link } from 'react-router-dom'
import { ATTENTION_GROUPS, attentionGroup, attentionRows, attentionState, attentionSummary, useAttention } from '../attention'
import { useEffect } from 'react'
import { useLocation } from 'react-router-dom'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'
import { lastSeen, relTime } from '../format'
import type { AttentionCoverage, AttentionItem } from '../wire.gen'
import './Attention.css'

// A source with no reading yet has never been checked: it is neither working nor failed.
const neverChecked = (at?: string) => !at || at.startsWith('0001-')
// A failed attempt newer than the last good reading is said as when it was last tried.
export const observed = (at: string, stale = false, attemptedAt?: string) => !neverChecked(attemptedAt) && (neverChecked(at) || Date.parse(attemptedAt!) > Date.parse(at))
  ? `last tried ${relTime(attemptedAt!) || 'at an unknown time'}`
  : neverChecked(at) ? 'not checked yet' : stale ? lastSeen(at) : `checked ${relTime(at) || 'at an unknown time'}`

// The server prefixes each detail with the name its title already carries
// ("built-in-docker: …" under "built-in-docker: resource missing"); the row
// shows the name once.
export function withoutRepeatedName(title: string, detail: string) {
  const m = /^([^:]{1,80}): (.*)$/s.exec(detail)
  return m && title.includes(m[1]) ? m[2].charAt(0).toUpperCase() + m[2].slice(1) : detail
}

function Detail({ text }: { text: string }) {
  const lines = detailLines(text)
  return lines.length > 1
    ? <ul className="attention-lines muted small">{lines.map(l => <li key={l}>{l}</li>)}</ul>
    : <div className="muted small">{text}</div>
}

// A detail that reports several named things in one sentence run
// ("a: could not be read. b: could not be read.") reads as one line each.
export function detailLines(detail: string) {
  const parts = detail.split(/(?<=\.)\s+(?=[\w.-]+: )/)
  return parts.length > 1 && parts.every(p => /^[\w.-]+: /.test(p)) ? parts : [detail]
}

const word = (item: AttentionItem) => item.unknown ? 'unknown' : item.statusLabel === 'low' ? 'low' : item.severity === 'advisory' ? 'not checked yet' : 'failed'

// Identical conditions (the same title, words and state) are one row with a
// count; the row expands to each one's link.
function bunch(items: AttentionItem[]) {
  const out: AttentionItem[][] = []
  for (const item of items) {
    const same = out.find(g => g[0].title === item.title && g[0].detail === item.detail && word(g[0]) === word(item) && g[0].stale === item.stale)
    if (same) same.push(item); else out.push([item])
  }
  return out
}

function ItemRow({ items }: { items: AttentionItem[] }) {
  const item = items[0]
  return <article className="card row attention-row">
    <span className="row-status"><Status word={word(item)} staleAt={item.stale && !item.unknown ? item.observedAt : undefined} /></span>
    <span className="attention-what">
      <Link to={item.href}>{item.title}</Link>{items.length > 1 && <span className="muted"> · {items.length}</span>}
      <Detail text={withoutRepeatedName(item.title, item.detail)} />
      {items.length > 1 && <details className="small"><summary>Show all {items.length}</summary>
        <ul className="attention-lines">{items.map((i, n) => <li key={i.key}><Link to={i.href}>{i.title} ({n + 1})</Link> <span className="muted">{i.key}</span></li>)}</ul>
      </details>}
    </span>
    {/* The chip already says "last seen" for a stale low or advisory reading. */}
    {!(item.stale && !item.unknown && word(item) === 'low') && <small className="muted attention-seen">{observed(item.observedAt, item.stale, item.attemptedAt)}</small>}
    <span className="row-action"><Link className="btn" to={item.href}>Open</Link></span>
  </article>
}

function SourceRow({ source }: { source: AttentionCoverage }) {
  return <article className="card row attention-row">
    <span className="row-status"><Status word="unknown" /></span>
    <span className="attention-what">
      <Link to={source.href}>{source.name}</Link>
      <Detail text={source.reason || 'No usable reading has been recorded yet.'} />
      <div className="muted small">{source.nextStep}</div>
    </span>
    <small className="muted attention-seen">{observed(source.observedAt, source.stale, source.attemptedAt)}</small>
    <span className="row-action"><Link className="btn" to={source.href}>Open</Link></span>
  </article>
}

export default function AttentionPage() {
  const q = useAttention()
  const state = attentionState(q)
  const { hash } = useLocation()
  const ready = !!q.data
  useEffect(() => { if (ready && hash) document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' }) }, [ready, hash])
  return <div>
    <div className="row"><h1 style={{ flex: 1, margin: 0 }}>Attention</h1></div>
    <ReadStatus q={q} what="conditions that need action" />
    <StaleNote q={q} />
    {q.data && <>
      <p className="muted attention-summary" role="status">{attentionSummary(state.count, state.unknown)}</p>
      {ATTENTION_GROUPS.map(g => {
        const items = attentionRows(q.data!).items.filter(i => attentionGroup(i.key) === g.key)
        const unknown = attentionRows(q.data!).sources.filter(s => attentionGroup(s.key) === g.key)
        if (!items.length && !unknown.length) return null
        // A top-bar light opens the page at its group.
        return <div key={g.key} id={g.key} className="attention-group"><Section title={g.title} count={items.length + unknown.length}>
          {bunch(items).map(same => <ItemRow key={same[0].key} items={same} />)}
          {unknown.map(source => <SourceRow key={source.key} source={source} />)}
        </Section></div>
      })}
      <Section title="All checks" count={q.data.sources.length}>
        {q.data.sources.map(source => <div key={source.key} className="card row attention-row">
          <span className="row-status"><Status word={source.unknown ? 'unknown' : neverChecked(source.observedAt) ? 'not checked yet' : 'fresh'} observation staleAt={source.stale && !source.unknown ? source.observedAt : undefined} /></span>
          <span className="attention-what"><Link to={source.href}>{source.name}</Link></span>
          {(!source.stale || source.unknown) && (!neverChecked(source.observedAt) || !neverChecked(source.attemptedAt)) && <small className="muted attention-seen">{observed(source.observedAt, source.stale, source.attemptedAt)}</small>}
        </div>)}
      </Section>
    </>}
  </div>
}
