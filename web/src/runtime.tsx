import type { CSSProperties, ReactNode } from 'react'
import { relTime } from './format'

export type RuntimeObservation = {
  // running, stopped or unknown, as the forge words it.
  observation: string
  observationIssue?: string
  observedAt?: string
  observationAttemptedAt?: string
  observationStale: boolean
}

// An observation from a reader that also says whether anything is deployed
// (the projects list). false: there is nothing to observe. Absent: the
// reader did not say, and the observation decides.
export type ProjectRuntime = RuntimeObservation & { deployed?: boolean }

export function runtimeDescription(o: ProjectRuntime): string {
  if (o.deployed === false) return 'not deployed yet'
  if (!o.observedAt) return 'status unknown · not checked yet'
  const state = o.observation === 'unknown' ? 'status unknown' : o.observation
  return `${o.observationStale ? 'Last known: ' : ''}${state} · checked ${relTime(o.observedAt)}${o.observationIssue ? ' · refresh unavailable' : ''}`
}

// The chip tone for a runtime observation. Not deployed yet is a plain
// neutral stamp: nothing is meant to be running, so nothing is unknown.
// Only a current observation is
// drawn as running (pass) or stopped (neutral); a stale or unknown one gets
// the unknown stamp, so "nobody knows" never looks like "stopped".
export function runtimeChip(o: { observation: string, observationStale: boolean, deployed?: boolean }): string {
  if (o.deployed === false) return ''
  if (o.observationStale || o.observation === 'unknown') return 'unknown'
  return o.observation === 'running' ? 'pass' : ''
}

export function RuntimeStatus({ observation }: { observation: ProjectRuntime }) {
  return <span className={`chip ${runtimeChip(observation)}`}>
    {runtimeDescription(observation)}
  </span>
}

// AppAddress: where an app answers, as a link, unless the forge says the
// address answers on the forge host only (no domain for it, so it is a port
// the forge's own Docker publishes on that host's loopback). Then it is not
// a link, which would fail for anyone elsewhere: it says where it works, at
// the port on that host.
export function AppAddress({ url, hostOnly, className, style, title, children }: {
  url: string; hostOnly?: boolean; className?: string; style?: CSSProperties; title?: string; children?: ReactNode
}) {
  if (hostOnly) {
    return (
      <span className={className} style={style} title={`${url} answers on the forge host only`}>
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
  return port ? `on the forge host only, at localhost:${port}` : 'on the forge host only'
}
