import { useEffect, useRef } from 'react'
import type { DisplayStatus, RuntimeState, HealthStatus, CheckLabel, ReleaseLabel, UpdateLabel, CopyLabel, JobLabel, TargetLabel, DepartureLabel, ResourceLabel, ProgressLabel, ADRLabel } from './wire.gen'

export const STATUS_LIGHT = {
  'not set up': '', low: 'pending', blocked: 'fail', running: 'pass', working: 'running', deploying: 'running', 'waiting for you': 'pending', 'waiting for checks': 'pending', stopped: '', unknown: 'unknown', unreachable: 'fail',
  waiting: 'pending', failed: 'fail', landed: 'pass', finished: 'pass', deployed: 'pass', passed: 'pass', excepted: 'overridden', 'not applicable': '', none: '', current: 'pass', available: 'pending', fresh: 'pass', overdue: 'fail', missing: 'fail', disposable: '',
} as const satisfies Record<'low' | 'blocked' | DisplayStatus | RuntimeState | HealthStatus | CheckLabel | ReleaseLabel | UpdateLabel | CopyLabel | JobLabel | TargetLabel | DepartureLabel | ResourceLabel | ProgressLabel | ADRLabel, string>

// The server supplies the word; help stays beside the chip on touch and keyboard.
export function Status({ word, tone, help, text }: { text?: string; word: keyof typeof STATUS_LIGHT; tone: string; help?: string }) {
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
  const chip = <span className={`chip ${tone}`} title={help} aria-label={help ? `${text ?? word}: ${help}` : undefined}>{tone && <span aria-hidden="true">● </span>}{text ?? word}</span>
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
