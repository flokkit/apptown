import { forwardRef, useLayoutEffect, useRef, useState, type ComponentPropsWithoutRef, type ReactNode } from 'react'
import { DetailsPopover } from './Popover'
import './styles/status-details.css'
import { lastSeen } from './format'
import type { DisplayStatus, RuntimeState, HealthStatus, CheckLabel, ReleaseLabel, UpdateLabel, CopyLabel, JobLabel, TargetLabel, DepartureLabel, ResourceLabel, ProgressLabel, ADRLabel, AttentionLabel } from './wire.gen'


// Recorded owner labels are inputs, never page-local chip wording.
export const STATUS_WORD = {
  'recovery not rehearsed': 'recovery not rehearsed', 'recovery not tested on its platform': 'recovery not tested on its platform',
  'not set up': 'not configured', low: 'low', blocked: 'blocked', running: 'in progress', working: 'in progress', deploying: 'deploying',
  'waiting for you': 'waiting for you', 'waiting for checks': 'waiting for checks', stopped: 'stopped', unknown: 'unknown', unreachable: 'unreachable',
  'waiting for approval': 'waiting for approval', cancelled: 'cancelled', superseded: 'superseded', waiting: 'waiting', failed: 'failed', landed: 'landed', finished: 'finished', deployed: 'deployed', passed: 'passed', excepted: 'excepted',
  'not applicable': 'not applicable', none: 'not configured', current: 'current', available: 'update available', fresh: 'working', overdue: 'failed', missing: 'failed', disposable: 'not applicable',
  'not checked yet': 'not checked yet', 'no automatic check': 'no automatic check', 'in progress': 'in progress', abandoned: 'abandoned',
  incomplete: 'incomplete', 'nothing connected': 'nothing connected', 'needs review': 'needs review',
  // A change worked in a chat that has no run yet: nobody is blocked on it.
  'chat open': 'chat open',
} as const satisfies Record<'chat open' | 'no automatic check' | 'in progress' | 'abandoned' | 'not checked yet' | 'low' | 'blocked' | 'incomplete' | 'nothing connected' | DisplayStatus | RuntimeState | HealthStatus | CheckLabel | ReleaseLabel | UpdateLabel | CopyLabel | JobLabel | TargetLabel | DepartureLabel | ResourceLabel | ProgressLabel | ADRLabel | AttentionLabel, string>

export const STATUS_LIGHT: Record<keyof typeof STATUS_WORD, string> = {
  'recovery not rehearsed': '', 'recovery not tested on its platform': '',
  'not set up': '', low: 'pending', blocked: 'pending', running: 'running', working: 'running', deploying: 'running',
  'waiting for you': 'pending', 'waiting for checks': 'pending', stopped: '', unknown: 'unknown', unreachable: 'fail',
  'waiting for approval': 'pending', cancelled: 'pending', superseded: 'pending', waiting: 'pending', failed: 'fail', landed: 'pass', finished: 'pass', deployed: 'pass', passed: 'pass', excepted: 'overridden',
  'not applicable': '', none: '', current: 'pass', available: 'pending', fresh: 'pass', overdue: 'fail', missing: 'fail', disposable: '',
  // Not checked yet is a fact about us, not a warning about the thing.
  'not checked yet': '', 'no automatic check': '', 'in progress': 'running', abandoned: '',
  // A finished piece of work that could not do all of it: not a failure, not a pass.
  incomplete: 'pending',
  'nothing connected': '',
  'chat open': '',
  // An AI flag no person has confirmed: amber, like work waiting on someone.
  'needs review': 'pending',
}

// Health observations share operational words; work and evidence retain their
// owner's labels. Freshness classification is supplied by the owner.
// reason, for work waiting on a person, replaces the word with why it is
// waiting ("a check failed"); the light still says which state it is.
// Status is a status chip: its light and word. Its details (help: why, and
// when it was checked) never sit beside it on the page; they open on hover
// or tap, so a page carries the word and a person asks for the rest. button
// makes the chip that control; the details panel uses it.
export function Status({ word, help, staleAt, observation = false, old = false, reason, button }: { word: keyof typeof STATUS_WORD; help?: string; staleAt?: string; observation?: boolean; old?: boolean; reason?: string; button?: import('react').ComponentPropsWithRef<'button'> }) {
  const operational = observation && ['running', 'working', 'passed', 'current', 'fresh'].includes(word) ? 'working' : STATUS_WORD[word]
  const label = staleAt && !['failed', 'unreachable', 'unknown', 'not set up', 'not applicable', 'not checked yet', 'none'].includes(word) ? lastSeen(staleAt) : old ? `old · ${operational}` : operational
  // An old reading is amber, except a failure: an old failure is still a failure.
  const tone = STATUS_LIGHT[word] === 'fail' ? 'fail' : old || staleAt && label.startsWith('last seen') ? 'pending' : observation && ['working', 'running'].includes(word) ? 'pass' : STATUS_LIGHT[word]
  const content = <>{/* Every state has its dot; a neutral one is grey, so it sits in line with the rest. */}<span aria-hidden="true">● </span><span className="chip-word">{reason ?? label}</span></>
  if (button) return <button type="button" {...button} className={`chip chip-button ${tone}`}>{content}</button>
  if (!help) return <span className={`chip ${tone}`}>{content}</span>
  return <StatusDetails tone={tone} help={help} label={label}>{content}</StatusDetails>
}

// Inside a link (or another control) a status keeps that control's one act:
// it is the chip with its details as its title, and the linked page says
// them in full. Elsewhere it opens its details.
function StatusDetails({ tone, help, label, children }: { tone: string; help: string; label: string; children: ReactNode }) {
  const at = useRef<HTMLSpanElement>(null)
  const [inControl, setInControl] = useState(false)
  useLayoutEffect(() => { setInControl(!!at.current?.parentElement?.closest('a, button, summary')) }, [])
  if (inControl) return <span ref={at} className={`chip ${tone}`} title={help} aria-label={`${label}: ${help}`}>{children}</span>
  return <><span ref={at} hidden /><DetailsPopover className="status-word" trigger={<StatusButton tone={tone} help={help} label={label}>{children}</StatusButton>}>
    <p className="status-word-detail">{help}</p>
  </DetailsPopover></>
}

const StatusButton = forwardRef<HTMLButtonElement, { tone: string; help: string; label: string; children: ReactNode } & ComponentPropsWithoutRef<'button'>>(function StatusButton({ tone, help, label, children, ...button }, ref) {
  return <button type="button" {...button} ref={ref} data-help={help} aria-label={`${label}: ${help}`} className={`chip chip-button ${tone}`}>{children}</button>
})

// StatusDot is a status's light without its word, for a row whose own text
// says the rest: the same marker the chip draws, its word read aloud.
export function StatusDot({ word, help }: { word: keyof typeof STATUS_WORD; help?: string }) {
  const label = STATUS_WORD[word]
  return <span className={`chip status-dot ${STATUS_LIGHT[word]}`} role="img" title={help} aria-label={help ? `${label}: ${help}` : label}><span aria-hidden="true">●</span></span>
}

// Change wording and tone are supplied by Workflow together.
export const waitingTone = (tone: string): string => ({ ok: 'pass', bad: 'fail', warn: 'pending', busy: 'running', neutral: '', unknown: 'unknown' })[tone] ?? 'unknown'
// As a button (an approval's status, which opens who can approve) it takes
// the Disclosure's ref and click.
export function ChangeStatus({ waiting, button }: { waiting: import('./wire.gen').WaitingOn; button?: import('react').ComponentPropsWithRef<'button'> }) {
  const content = <><span aria-hidden="true">● </span><span className="chip-word">{waiting.says}</span></>
  if (button) return <button type="button" {...button} className={`chip chip-button ${waitingTone(waiting.tone)}`}>{content}</button>
  return <span className={`chip ${waitingTone(waiting.tone)}`}>{content}</span>
}

export function AccountStatus({ disabled, acted }: { disabled?: boolean; acted?: boolean; hasPassword?: boolean }) {
  const state = disabled ? 'disabled' : acted ? 'active' : 'never signed in'
  return <span className={`list-status ${state === 'active' ? 'active' : 'inactive'}`}><span aria-hidden="true" />{state}</span>
}
