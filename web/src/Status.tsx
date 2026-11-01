import { useEffect, useRef } from 'react'
import { lastSeen } from './format'
import type { DisplayStatus, RuntimeState, HealthStatus, CheckLabel, ReleaseLabel, UpdateLabel, CopyLabel, JobLabel, TargetLabel, DepartureLabel, ResourceLabel, ProgressLabel, ADRLabel } from './wire.gen'

// Recorded owner labels are inputs, never page-local chip wording.
export const STATUS_WORD = {
  'not set up': 'not set up', low: 'low', blocked: 'blocked', running: 'in progress', working: 'in progress', deploying: 'deploying',
  'waiting for you': 'waiting for you', 'waiting for checks': 'waiting for checks', stopped: 'stopped', unknown: 'unknown', unreachable: 'failed',
  waiting: 'waiting', failed: 'failed', landed: 'landed', finished: 'finished', deployed: 'deployed', passed: 'passed', excepted: 'excepted',
  'not applicable': 'not applicable', none: 'not set up', current: 'current', available: 'update available', fresh: 'working', overdue: 'failed', missing: 'failed', disposable: 'not applicable',
  'not checked yet': 'not checked yet', 'no automatic check': 'no automatic check', 'in progress': 'in progress', abandoned: 'abandoned',
} as const satisfies Record<'no automatic check' | 'in progress' | 'abandoned' | 'not checked yet' | 'low' | 'blocked' | DisplayStatus | RuntimeState | HealthStatus | CheckLabel | ReleaseLabel | UpdateLabel | CopyLabel | JobLabel | TargetLabel | DepartureLabel | ResourceLabel | ProgressLabel | ADRLabel, string>

export const STATUS_LIGHT: Record<keyof typeof STATUS_WORD, string> = {
  'not set up': '', low: 'pending', blocked: 'pending', running: 'running', working: 'running', deploying: 'running',
  'waiting for you': 'pending', 'waiting for checks': 'pending', stopped: '', unknown: 'unknown', unreachable: 'fail',
  waiting: 'pending', failed: 'fail', landed: 'pass', finished: 'pass', deployed: 'pass', passed: 'pass', excepted: 'overridden',
  'not applicable': '', none: '', current: 'pass', available: 'pending', fresh: 'pass', overdue: 'fail', missing: 'fail', disposable: '',
  'not checked yet': 'pending', 'no automatic check': '', 'in progress': 'running', abandoned: '',
}

// Health observations share operational words; work and evidence retain their
// owner's labels. Freshness classification is supplied by the owner.
export function Status({ word, help, staleAt, observation = false, old = false }: { word: keyof typeof STATUS_WORD; help?: string; staleAt?: string; observation?: boolean; old?: boolean }) {
  const operational = observation && ['running', 'working', 'passed', 'current', 'fresh'].includes(word) ? 'working' : STATUS_WORD[word]
  const label = staleAt && !['failed', 'unreachable', 'unknown', 'not set up', 'not applicable', 'not checked yet', 'none'].includes(word) ? lastSeen(staleAt) : old ? `old · ${operational}` : operational
  const tone = old || staleAt && label.startsWith('last seen') ? 'pending' : observation && ['working', 'running'].includes(word) ? 'pass' : STATUS_LIGHT[word]
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
  const chip = <span className={`chip ${tone}`} title={help} aria-label={help ? `${label}: ${help}` : undefined}>{tone && <span aria-hidden="true">● </span>}{label}</span>
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
