import { PRODUCT } from './brand'
import type { CSSProperties, ReactNode } from 'react'
import { fmtStamp } from './format'

import type { ActivityDeployment, RuntimeState } from './wire.gen'
import { Status, STATUS_LIGHT } from './Status'

export type RuntimeObservation = Pick<ActivityDeployment, 'observation' | 'observationIssue' | 'observedAt' | 'observationAttemptedAt' | 'observationStale' | 'targetName' | 'unreachableSince'>

// Exhaustive against the generated server vocabulary. Classification belongs
// to the deployment owner, including expired samples and reachability.
export const RUNTIME_LIGHT: Record<RuntimeState, string> = STATUS_LIGHT

export function runtimeDescription(o: RuntimeObservation): string {
  if (o.observation === 'unreachable') return `${PRODUCT} can't reach ${o.targetName || 'the deployment host'}${o.unreachableSince ? ` since ${fmtStamp(o.unreachableSince)}` : ''}. The app may still be running.`
  if (o.observationIssue) return o.observationIssue
  return o.observedAt ? `Checked ${fmtStamp(o.observedAt)}.` : 'Not checked yet.'
}

export function runtimeChip(o: Pick<RuntimeObservation, 'observation'>): string {
  return RUNTIME_LIGHT[o.observation]
}

export function runtimeLabel(o: Pick<RuntimeObservation, 'observation'>): string {
  return o.observation
}

export function checkedWords(o: Pick<RuntimeObservation, 'observedAt' | 'observationStale'>): string {
  return o.observedAt ? `Checked ${fmtStamp(o.observedAt)}` : 'Not checked yet'
}

export function RuntimeStatus({ observation }: { observation: RuntimeObservation }) {
  return <Status word={observation.observation} tone={runtimeChip(observation)} help={runtimeDescription(observation)} />
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
