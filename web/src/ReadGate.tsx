import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import type { Read } from './data'
import { fmtStamp } from './format'
import { Status } from './Status'

// One place for what a read shows besides its answer: still on its way,
// refused, broken, or shown but possibly old. design/architecture/web-data.md states the display rules:
// a shown answer stays readable when a refresh fails, a failed read is never
// shown as empty, and a refusal is not an error.

// Gated is the part of a read the gate looks at, so a page can gate on any
// read whatever its answer's type.
export type Gated = Pick<Read<unknown>, 'data' | 'err' | 'reason' | 'loading' | 'denied' | 'refreshFailed' | 'answeredAt'>


// ReadStatus is what a read with no answer to show says: nothing while it
// has no path, then refused, failed or still reading, in that order. A read
// that holds an answer says nothing here; StaleNote marks it when it may be
// old.
export function ReadStatus({ q, what, denied, className = '' }: { q: Gated | Gated[]; what: string; denied?: ReactNode; className?: string }) {
  const missing = (Array.isArray(q) ? q : [q]).filter((r) => r.data === null || r.data === undefined)
  if (missing.length === 0) return null
  if (missing.some((r) => r.denied)) return <div className={`muted ${className}`}>{denied ?? `Not available to you: ${what}.`}</div>
  const failed = missing.find((r) => r.err)
  if (failed) return <div className={`error ${className}`}><Status word="unknown" tone="unknown" help={`${what.charAt(0).toUpperCase() + what.slice(1)} could not be read: ${failed.reason || failed.err}`} /></div>
  if (missing.some((r) => r.loading)) return <div className={`muted ${className}`}>Loading…</div>
  return null
}

// StaleNote marks, above an answer whose latest refresh failed, that what
// follows may be old: a compact badge with the time of the last good read.
// The read's own reason and the full time ride in its title and accessible
// name, never parsed from its error text. Inside a StaleScope it also counts
// towards the scope's one notice, so the sentence is not repeated per
// section.
export function StaleNote({ q, className = '', inline = false }: { q: Gated | Gated[]; className?: string; inline?: boolean }) {
  const old = (Array.isArray(q) ? q : [q]).find((r) => r.refreshFailed && r.data !== null && r.data !== undefined)
  const scope = useContext(StaleContext)
  const shown = !!old
  useEffect(() => (shown && scope ? scope.add() : undefined), [shown, scope])
  if (!old) return null
  const full = `Refresh failed (${old.reason}); showing what was read ${old.answeredAt ? fmtStamp(new Date(old.answeredAt).toISOString()) : 'earlier'}.`
  const mark = <Status word="unknown" tone="unknown" help={full} />
  return inline ? <> {mark}</> : <div className={`stale-note${className ? ` ${className}` : ''}`}>{mark}</div>
}

// StaleContext counts the stale notes shown inside one StaleScope.
const StaleContext = createContext<{ add: () => () => void } | null>(null)

// StaleScope says once, above its children, that some of what they show
// could not be refreshed, whenever any StaleNote inside it is shown. The app
// wraps each page in one; each section then carries only its badge.
export function StaleScope({ children }: { children: ReactNode }) {
  const [count, setCount] = useState(0)
  const scope = useMemo(() => ({
    add: () => {
      setCount((n) => n + 1)
      return () => setCount((n) => n - 1)
    },
  }), [])
  return (
    <StaleContext.Provider value={scope}>
      {count > 0 && <div className="warn-line small stale-notice" role="status">Refresh failed. Last answer shown.</div>}
      {children}
    </StaleContext.Provider>
  )
}

// ReadGate shows its children only once every read named has an answer, with
// a stale note when any of them may be old; until then it shows the reads'
// status. `children` may take the first read's answer, so a page need not
// assert it is there.
export function ReadGate<T>({ q, also = [], what, denied, className, children }: {
  q: Read<T> | Gated
  also?: Gated[]
  what: string
  denied?: ReactNode
  className?: string
  children?: ReactNode | ((data: T) => ReactNode)
}) {
  const all = [q, ...also]
  if (all.some((r) => r.data === null || r.data === undefined)) return <ReadStatus q={all} what={what} denied={denied} className={className} />
  return (
    <>
      <StaleNote q={all} className={className} />
      {typeof children === 'function' ? children(q.data as T) : children}
    </>
  )
}
