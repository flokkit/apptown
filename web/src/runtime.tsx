import { PRODUCT } from './brand'
import type { CSSProperties, ReactNode } from 'react'
import { relTime } from './format'

export type RuntimeObservation = {
  // running, stopped or unknown, as AppTown words it.
  observation: string
  observationIssue?: string
  observedAt?: string
  observationAttemptedAt?: string
  observationStale: boolean
}

export function runtimeDescription(o: RuntimeObservation): string {
  if (!o.observedAt) return 'status unknown · not checked yet'
  const state = o.observation === 'unknown' ? 'status unknown' : o.observation
  return `${o.observationStale ? 'Last known: ' : ''}${state} · checked ${relTime(o.observedAt)}${o.observationIssue ? ' · refresh unavailable' : ''}`
}

// The chip tone for a runtime observation. Only a current observation is
// drawn as running (pass) or stopped (neutral); a stale or unknown one gets
// the unknown stamp, so "nobody knows" never looks like "stopped".
export function runtimeChip(o: { observation: string, observationStale: boolean }): string {
  if (o.observationStale || o.observation === 'unknown') return 'unknown'
  return o.observation === 'running' ? 'pass' : ''
}

// runtimeLabel is a badge's words: the state alone while the reading is
// current, since every badge would otherwise repeat "checked just now". A
// reading that is out of date says so and how old it is. The full sentence,
// with when it was checked and whether a refresh failed, is the badge's
// title; the unknown style marks both stale and never-checked badges.
export function runtimeLabel(o: RuntimeObservation): string {
  if (!o.observedAt) return 'not checked yet'
  const state = o.observation === 'unknown' ? 'status unknown' : o.observation
  if (!o.observationStale) return state
  return `last known: ${state} · ${relTime(o.observedAt)}`
}

export function RuntimeStatus({ observation }: { observation: RuntimeObservation }) {
  return <span className={`chip ${runtimeChip(observation)}`} title={runtimeDescription(observation)}>
    {runtimeLabel(observation)}
  </span>
}

// AppAddress: where an app answers, as a link, unless AppTown says the
// address answers on the AppTown host only (no domain for it, so it is a port
// AppTown's own Docker publishes on that host's loopback). Then it is not
// a link, which would fail for anyone elsewhere: it says where it works, at
// the port on that host.
export function AppAddress({ url, hostOnly, className, style, title, children }: {
  url: string; hostOnly?: boolean; className?: string; style?: CSSProperties; title?: string; children?: ReactNode
}) {
  if (hostOnly) {
    return (
      <span className={className} style={style} title={`${url} answers on ${PRODUCT}'s host only`}>
        {children}{children ? ' ' : null}<span className="muted small">{hostOnlyWords(url)}</span>
      </span>
    )
  }
  return <a href={url} target="_blank" rel="noreferrer" className={className} style={style} title={title}>{children}</a>
}

// hostOnlyWords: where a host-only address works, in words.
export function hostOnlyWords(url: string): string {
  let port = ''
  try { port = new URL(url).port } catch { /* not a URL: say where, without the port */ }
  return port ? `on ${PRODUCT}'s host only, at localhost:${port}` : `on ${PRODUCT}'s host only`
}
