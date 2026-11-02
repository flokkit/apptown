import { useEffect, useRef } from 'react'
import { lastSeen } from './format'
import type { DisplayStatus, RuntimeState, HealthStatus, CheckLabel, ReleaseLabel, UpdateLabel, CopyLabel, JobLabel, TargetLabel, DepartureLabel, ResourceLabel, ProgressLabel, ADRLabel } from './wire.gen'

// Recorded owner labels are inputs, never page-local chip wording.
export const STATUS_WORD = {
  'not set up': 'not configured', low: 'low', blocked: 'blocked', running: 'in progress', working: 'in progress', deploying: 'deploying',
  'waiting for you': 'waiting for you', 'waiting for checks': 'waiting for checks', stopped: 'stopped', unknown: 'unknown', unreachable: 'unreachable',
  waiting: 'waiting', failed: 'failed', landed: 'landed', finished: 'finished', deployed: 'deployed', passed: 'passed', excepted: 'excepted',
  'not applicable': 'not applicable', none: 'not configured', current: 'current', available: 'update available', fresh: 'working', overdue: 'failed', missing: 'failed', disposable: 'not applicable',
  'not checked yet': 'not checked yet', 'no automatic check': 'no automatic check', 'in progress': 'in progress', abandoned: 'abandoned',
  incomplete: 'incomplete', 'nothing connected': 'nothing connected', 'needs review': 'needs review',
} as const satisfies Record<'no automatic check' | 'in progress' | 'abandoned' | 'not checked yet' | 'low' | 'blocked' | 'incomplete' | 'nothing connected' | DisplayStatus | RuntimeState | HealthStatus | CheckLabel | ReleaseLabel | UpdateLabel | CopyLabel | JobLabel | TargetLabel | DepartureLabel | ResourceLabel | ProgressLabel | ADRLabel, string>

export const STATUS_LIGHT: Record<keyof typeof STATUS_WORD, string> = {
  'not set up': '', low: 'pending', blocked: 'pending', running: 'running', working: 'running', deploying: 'running',
  'waiting for you': 'pending', 'waiting for checks': 'pending', stopped: '', unknown: 'unknown', unreachable: 'fail',
  waiting: 'pending', failed: 'fail', landed: 'pass', finished: 'pass', deployed: 'pass', passed: 'pass', excepted: 'overridden',
  'not applicable': '', none: '', current: 'pass', available: 'pending', fresh: 'pass', overdue: 'fail', missing: 'fail', disposable: '',
  // Not checked yet is a fact about us, not a warning about the thing.
  'not checked yet': '', 'no automatic check': '', 'in progress': 'running', abandoned: '',
  // A finished piece of work that could not do all of it: not a failure, not a pass.
  incomplete: 'pending',
  'nothing connected': '',
  // An AI flag no person has confirmed: amber, like work waiting on someone.
  'needs review': 'pending',
}

// Health observations share operational words; work and evidence retain their
// owner's labels. Freshness classification is supplied by the owner.
// reason, for work waiting on a person, replaces the word with why it is
// waiting ("a check failed"); the light still says which state it is.
export function Status({ word, help, staleAt, observation = false, old = false, reason }: { word: keyof typeof STATUS_WORD; help?: string; staleAt?: string; observation?: boolean; old?: boolean; reason?: string }) {
  const operational = observation && ['running', 'working', 'passed', 'current', 'fresh'].includes(word) ? 'working' : STATUS_WORD[word]
  const label = staleAt && !['failed', 'unreachable', 'unknown', 'not set up', 'not applicable', 'not checked yet', 'none'].includes(word) ? lastSeen(staleAt) : old ? `old · ${operational}` : operational
  // An old reading is amber, except a failure: an old failure is still a failure.
  const tone = STATUS_LIGHT[word] === 'fail' ? 'fail' : old || staleAt && label.startsWith('last seen') ? 'pending' : observation && ['working', 'running'].includes(word) ? 'pass' : STATUS_LIGHT[word]
  const ref = useRef<HTMLDetailsElement>(null)
  useEffect(() => {
    const closeOutside = (event: PointerEvent) => {
      if (!ref.current?.contains(event.target as Node) && ref.current) ref.current.open = false
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && ref.current?.open) {
        ref.current.open = false
        ref.current.querySelector('summary')?.focus()
      }
    }
    document.addEventListener('pointerdown', closeOutside, true)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('pointerdown', closeOutside, true)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [])
  const chip = <span className={`chip ${tone}`} title={help} aria-label={help ? `${label}: ${help}` : undefined}>{/* Every state has its dot; a neutral one is grey, so it sits in line with the rest. */}<span aria-hidden="true">● </span>{reason ?? label}</span>
  if (!help) return chip
  return <details ref={ref} className="status-help" onClick={(e) => e.stopPropagation()}>
    <summary onClick={(e) => {
      // A linked row must open its help without following the row's link.
      e.preventDefault()
      e.stopPropagation()
      const details = e.currentTarget.parentElement as HTMLDetailsElement
      if (!details.open) {
        document.querySelectorAll<HTMLDetailsElement>('.status-help[open]').forEach(other => { other.open = false })
        const help = details.querySelector('div')!
        const width = Math.min(512, window.innerWidth - 32)
        const left = details.getBoundingClientRect().left
        help.style.left = `${Math.max(16 - left, Math.min(0, window.innerWidth - 16 - left - width))}px`
      }
      details.open = !details.open
    }}>{chip}</summary>
    <div className="small muted">{help}</div>
  </details>
}
