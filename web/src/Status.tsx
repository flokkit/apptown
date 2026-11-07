import { useContext } from 'react'
import { lastSeen } from './format'
import type { DisplayStatus, RuntimeState, HealthStatus, CheckLabel, ReleaseLabel, UpdateLabel, CopyLabel, JobLabel, TargetLabel, DepartureLabel, ResourceLabel, ProgressLabel, ADRLabel, AttentionLabel } from './wire.gen'

import { InlineHelp } from './InlineHelp'

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
export function Status({ word, help, staleAt, observation = false, old = false, reason, stacked = false }: { word: keyof typeof STATUS_WORD; help?: string; staleAt?: string; observation?: boolean; old?: boolean; reason?: string; stacked?: boolean }) {
  const inlineHelp = useContext(InlineHelp)
  const operational = observation && ['running', 'working', 'passed', 'current', 'fresh'].includes(word) ? 'working' : STATUS_WORD[word]
  const label = staleAt && !['failed', 'unreachable', 'unknown', 'not set up', 'not applicable', 'not checked yet', 'none'].includes(word) ? lastSeen(staleAt) : old ? `old · ${operational}` : operational
  // An old reading is amber, except a failure: an old failure is still a failure.
  const tone = STATUS_LIGHT[word] === 'fail' ? 'fail' : old || staleAt && label.startsWith('last seen') ? 'pending' : observation && ['working', 'running'].includes(word) ? 'pass' : STATUS_LIGHT[word]
  const chip = <span className={`chip ${tone}`} title={help} aria-label={help ? `${label}: ${help}` : undefined}>{/* Every state has its dot; a neutral one is grey, so it sits in line with the rest. */}<span aria-hidden="true">● </span>{reason ?? label}</span>
  if (!help) return chip
  return <span className={stacked ? 'status-help-stack' : inlineHelp ? 'status-inline' : 'status-help-inline'}>{chip}<span className="small muted">{help}</span></span>
}

// Change wording and tone are supplied by Workflow together.
export const waitingTone = (tone: string): string => ({ ok: 'pass', bad: 'fail', warn: 'pending', busy: 'running', neutral: '', unknown: 'unknown' })[tone] ?? 'unknown'
export function ChangeStatus({ waiting }: { waiting: import('./wire.gen').WaitingOn }) {
  return <span className={`chip ${waitingTone(waiting.tone)}`}><span aria-hidden="true">● </span>{waiting.says}</span>
}

export function AccountStatus({ disabled, acted }: { disabled?: boolean; acted?: boolean; hasPassword?: boolean }) {
  const state = disabled ? 'disabled' : acted ? 'active' : 'never signed in'
  return <span className={`list-status ${state === 'active' ? 'active' : 'inactive'}`}><span aria-hidden="true" />{state}</span>
}
