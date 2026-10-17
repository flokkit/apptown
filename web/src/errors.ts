import * as Sentry from '@sentry/browser'
import type { BrowserErrorReporting } from './wire.gen'

// The forge's own web UI reports its exceptions to the same GlitchTip
// project forged reports to (self_errors.go): uncaught errors, unhandled
// promise rejections, and render faults the ErrorBoundary catches. The
// DSN comes from the forge — a DSN is public by design — and is empty
// while the forge project is unattached, in which case nothing is
// initialised and nothing is sent. No tracing, no replays: errors only.
let ready = false

export async function bootErrorReporting() {
  try {
    const res = await fetch('/api/v1/errors/browser', { credentials: 'same-origin' })
    if (!res.ok) return
    const { dsn, release } = (await res.json()) as BrowserErrorReporting
    if (!dsn) return
    Sentry.init(reportingOptions(dsn, release))
    Sentry.setTag('surface', 'web-ui')
    ready = true
  } catch {
    // reporting is best effort; the UI must never depend on it
  }
}

// The options every report is sent under. An invite or password reset link
// carries a live credential in its URL (/invite/forge_invite_…); the invite
// page moves it out of the address bar, and every report is scrubbed of any
// such token wherever it appears — the page URL, breadcrumbs, messages — in
// case one is captured before or despite that.
export function reportingOptions(dsn: string, release?: string): Sentry.BrowserOptions {
  return {
    dsn,
    release: release ? `forge-ui@${release}` : undefined,
    environment: window.location.hostname,
    sendDefaultPii: false,
    tracesSampleRate: 0,
    beforeSend: (event) => scrubLinkTokens(event),
    beforeBreadcrumb: (crumb) => scrubLinkTokens(crumb),
  }
}

const linkToken = /forge_invite_[A-Za-z0-9]+/g

// scrubLinkTokens replaces every invite or password reset token in a report
// with a placeholder, wherever in the report it sits.
export function scrubLinkTokens<T>(value: T): T {
  const text = JSON.stringify(value)
  if (text === undefined || !text.includes('forge_invite_')) return value
  return JSON.parse(text.replace(linkToken, 'forge_invite_[removed]')) as T
}

// reportRenderFault: what the ErrorBoundary calls; a no-op until booted.
export function reportRenderFault(error: Error, componentStack?: string) {
  if (!ready) return
  Sentry.captureException(error, { contexts: { react: { componentStack } } })
}
