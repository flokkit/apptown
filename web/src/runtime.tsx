import { PRODUCT } from './brand'
import { Fragment, type CSSProperties, type ReactNode } from 'react'
import { fmtStamp, fmtLastRead } from './format'

import type { ActivityDeployment, DeploymentStatus } from './wire.gen'
import { Status } from './Status'

export type RuntimeObservation = Pick<ActivityDeployment, 'observation' | 'observationIssue' | 'observedAt' | 'observationAttemptedAt' | 'observationStale' | 'targetName' | 'unreachableSince' | 'status'>

export function checkedWords(o: Pick<RuntimeObservation, 'observedAt' | 'observationStale'>): string {
  return o.observedAt ? `Checked ${fmtStamp(o.observedAt)}` : 'Not checked yet'
}

// A failed browser refresh qualifies the displayed read, not the host. The
// owner's answer remains intact in the cache and is shown again on recovery.
export function failedStatusRefresh(status: DeploymentStatus, reason: string): DeploymentStatus {
  return { ...status, word: 'unknown', reason: `The status could not be refreshed: ${reason}`, stale: true }
}

export function failedRuntimeRefresh<T extends RuntimeObservation>(answer: T, reason: string): T {
  return { ...answer, observation: 'unknown', observationStale: true, observationIssue: reason, status: failedStatusRefresh(answer.status, reason) }
}

// The browser formats the owner's answer; it never qualifies runtime evidence.
export function statusProps(status: DeploymentStatus) {
  const checked = status.checkedAt ? `checked ${fmtLastRead(Date.parse(status.checkedAt))}` : 'not checked yet'
  const retry = status.retryAt ? ` · next check ${new Date(status.retryAt).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}` : ''
  return { word: status.word, stacked: true, help: `${status.reason ? `${status.reason} · ` : ''}${checked}${status.stale ? ' · old reading' : ''}${retry}` }
}

export function RuntimeStatus({ observation }: { observation: RuntimeObservation }) {
  return <Status observation {...statusProps(observation.status)} />
}

// Without a label, show the public host and path with shared styling and wrap
// points after dots and slashes. Explicit labels keep their own treatment.
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
  const address = children === undefined
  return <a href={url} target="_blank" rel="noopener noreferrer"
    className={address ? ['app-address', className].filter(Boolean).join(' ') : className}
    style={style} title={title}>
    {address ? url.replace(/^https?:\/\//, '').split(/(?<=[./])/).map((part, i) => <Fragment key={i}>{i > 0 && <wbr />}{part}</Fragment>) : children}
  </a>
}

// hostOnlyWords: where a host-only address works, in words.
export function hostOnlyWords(url: string): string {
  let port = ''
  try { port = new URL(url).port } catch { /* not a URL: say where, without the port */ }
  return port ? `on ${PRODUCT}'s host only, at localhost:${port}` : `on ${PRODUCT}'s host only`
}
