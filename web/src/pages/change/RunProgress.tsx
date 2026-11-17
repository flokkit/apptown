import { useCallback, useEffect, useId, useRef, useState } from 'react'
import type { ChangeView, Step } from '../../api'
import type { WaitingOn } from '../../wire.gen'
import { Popover, hoverDelay } from '../../Popover'
import { StaleNote, type Gated } from '../../ReadGate'
import { currentSteps, Now, useNow } from './Now'
import './RunProgress.css'

export function segmentState(step: Step, waiting: WaitingOn): string {
  return step.progress === 'running' || step.progress === 'waiting' && step.id === waiting.step && step.runId === waiting.run ? 'current' : step.progress
}
export function stageState(steps: Step[], waiting: WaitingOn): string {
  const states = steps.map(s => segmentState(s, waiting)).filter(s => s !== 'not_applicable')
  if (!steps.length) return 'waiting'
  return ['failed', 'unknown', 'skipped', 'current', 'waiting', 'done'].find(state => states.includes(state)) ?? 'not_applicable'
}

export function RunProgress({ view, reading }: { view: ChangeView; reading?: Gated }) {
  const now = useNow(view)
  const anchor = useRef<HTMLButtonElement>(null)
  const id = useId()
  const [open, setOpen] = useState<false | 'hover' | 'focus'>(false)
  const timer = useRef<ReturnType<typeof setTimeout>>()
  const restoring = useRef(false)
  const close = useCallback(() => {
    clearTimeout(timer.current)
    restoring.current = true
    setOpen(false)
    queueMicrotask(() => { restoring.current = false })
  }, [])
  useEffect(() => () => clearTimeout(timer.current), [])
  if (!view.detail) return null
  const root = view.detail
  const active = now.details.at(-1)!
  const steps = currentSteps(active)
  const position = steps.findIndex(s => s.id === view.waitingOn.step) + 1 || steps.length
  const stage = (steps[0]?.stage ?? active.run.currentStage).replace(/-/g, ' ')
  const enter = (event: React.PointerEvent) => {
    if (event.pointerType !== 'mouse') return
    clearTimeout(timer.current)
    timer.current = setTimeout(() => setOpen(value => value || 'hover'), open ? 0 : hoverDelay)
  }
  const leave = (event: React.PointerEvent) => {
    if (event.pointerType !== 'mouse') return
    clearTimeout(timer.current)
    timer.current = setTimeout(() => setOpen(value => value === 'hover' ? false : value), 150)
  }
  return <div className="ch-progress" onPointerEnter={enter} onPointerLeave={leave}>
    <button ref={anchor} className="ch-progress-trigger" aria-label={`Progress: ${stage} step ${position} of ${steps.length}`} aria-expanded={!!open} aria-controls={open ? id : undefined}
      onFocus={() => { if (!restoring.current) setOpen('focus') }}
      onClick={() => setOpen('focus')} onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); setOpen('focus') } else if (event.key === 'Escape') { event.preventDefault(); close() } }}>
      <span className="ch-progress-bar" aria-label="Stages">{root.stages.map(stage => <span key={stage} className={`ch-progress-segment ${stageState(root.steps.filter(s => s.stage === stage), view.waitingOn)}`} title={stage.replace(/-/g, ' ')} />)}</span>
      <span className="ch-progress-bar" aria-label="Steps">{steps.map(s => <span key={s.id} className={`ch-progress-segment ${segmentState(s, view.waitingOn)}`} />)}</span>
    </button>
    <div hidden={!open}>{now.readers}</div>
    {open && <Popover key={open} anchor={anchor} onClose={close} id={id} className="ch-progress-popover" align="start" takeFocus={open === 'focus'} onPointerEnter={enter} onPointerLeave={leave}>
      {reading && <StaleNote q={reading} />}
      <Now view={view} details={now.details} readings={now.readings} />
    </Popover>}
  </div>
}
