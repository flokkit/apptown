// @vitest-environment jsdom
import css from './deploy-scene.css?inline'
import cardCss from '../project/deployment-deploys.css?inline'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { DeployScene, deployWords } from './DeployScene'
import type { DeployStage } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
it.each(Object.keys(deployWords) as DeployStage[])('draws only the reported %s frame and names it in words', stage => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage={stage} outcome="running" />)
  const index = ['preparing', 'building', 'starting', 'checking', 'live'].indexOf(stage === 'backing-up' ? 'preparing' : stage) + 1
  expect(el.querySelector('.deploy-scene')?.classList.contains(`g${index}`)).toBe(true)
  expect(el.querySelector('.deploy-scene')?.classList.contains(`g${index + 1}`)).toBe(false)
  expect(el.querySelector('[role="img"]')?.getAttribute('aria-label')).toContain(deployWords[stage])
  expect(el.querySelector('.celebrate')).toBeNull()
  expect(el.querySelector('[aria-current="step"]')?.textContent?.toLowerCase()).toContain(deployWords[stage])
})
it('freezes unreadable progress and uses the recorded failure stage', () => {
  const paused = renderToStaticMarkup(<DeployScene stage="building" outcome="paused" />)
  expect(paused).toContain('g2 n2 paused')
  const failed = renderToStaticMarkup(<DeployScene stage="checking" failedStage="starting" outcome="failed" />)
  expect(failed).toContain('g1 g2 g3')
  expect(failed).not.toContain('g4')
  expect(failed).toContain('Stopped while starting the app')
  expect(failed).toContain('fail')
  expect(failed).not.toContain('n3')
})
it('never celebrates on an already-live mount; celebrates once after observing running', () => {
  const el = document.createElement('div'), root = createRoot(el)
  const draw = (stage: DeployStage, outcome: 'passed' | 'running') => act(() => root.render(<DeployScene stage={stage} outcome={outcome} />))
  try {
    draw('live', 'passed')
    expect(el.querySelector('.celebrate')).toBeNull()
    draw('checking', 'running')
    draw('live', 'passed')
    expect(el.querySelector('.celebrate')).not.toBeNull()
    act(() => {
      const e = new Event('webkitAnimationEnd', { bubbles: true })
      Object.defineProperty(e, 'animationName', { value: 'deploy-burst' })
      el.querySelector('.spark')!.dispatchEvent(e)
    })
    draw('live', 'passed')
    expect(el.querySelector('.celebrate')).toBeNull()
  } finally { act(() => root.unmount()) }
})

it('holds the last reported frame when the feed closes before the change read refreshes', () => {
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<DeployScene stage="building" outcome="running" />))
    act(() => root.render(<DeployScene outcome="paused" />))
    expect(el.querySelector('.g2.paused')).not.toBeNull()
    expect(el.querySelector('[role="img"]')?.getAttribute('aria-label')).toContain('building the app')
    expect(el.querySelector('.g3')).toBeNull()
  } finally { act(() => root.unmount()) }
})

it.each(['starting', 'checking', 'live'] as const)('shows the cumulative windows from starting onwards (%s)', stage => {
  const style = document.createElement('style')
  style.textContent = css.slice(css.indexOf('/* Deploy scenes'), css.indexOf('@keyframes deploy-fade'))
  document.head.append(style)
  const el = document.createElement('div')
  document.body.append(el)
  try {
    for (const outcome of ['running', 'failed', 'passed'] as const) {
      if (outcome === 'passed' && stage !== 'live') continue
      el.innerHTML = renderToStaticMarkup(<DeployScene stage={stage} failedStage={stage} outcome={outcome} />)
      expect(getComputedStyle(el.querySelector('.win')!).opacity).toBe('1')
    }
  } finally { el.remove(); style.remove() }
})

it('shows the backup stage between preparing and building, including a stopped copy', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage="backing-up" outcome="running" />)
  const rows = [...el.querySelectorAll('.deploy-stages li')].map(row => row.textContent)
  expect(rows[0]).toContain('Getting the code ready')
  expect(rows[1]).toContain('Backing up the datanow')
  expect(rows[2]).toContain('Building the app')
  el.innerHTML = renderToStaticMarkup(<DeployScene failedStage="backing-up" outcome="failed" />)
  expect(el.querySelectorAll('.deploy-stages li')[1].textContent).toContain('Backing up the datastopped')
})

it('marks finished, running, unreached and stopped stages beside a separate drawing panel', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage="starting" outcome="running" />)
  const rows = [...el.querySelectorAll('.deploy-stages li')]
  expect(rows.map(row => row.querySelector('.deploy-stage-mark')?.textContent)).toEqual(['✓', '✓', '✓', '', '·', '·'])
  expect(rows[3].querySelector('[aria-label="running"]')).not.toBeNull()
  expect(rows[3].textContent).toContain('Starting the appnow')
  expect(el.querySelector('.deploy-scene-panel + .deploy-stages')).not.toBeNull()
  el.innerHTML = renderToStaticMarkup(<DeployScene failedStage="starting" outcome="failed" />)
  expect(el.querySelector('.deploy-stage-mark.bad')?.textContent).toBe('✗')
})
it('carries blocks whenever the server reports building, including a running mount', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage="building" outcome="running" />)
  expect(el.querySelectorAll('.delivery')).toHaveLength(7)
  expect(el.querySelectorAll('.delivery .cable')).toHaveLength(7)
})
it('keeps the door shut for checking and opens it only for live', () => {
  const style = document.createElement('style')
  style.textContent = css.slice(css.indexOf('/* Deploy scenes'), css.indexOf('@keyframes deploy-fade'))
  document.head.append(style)
  const el = document.createElement('div'); document.body.append(el)
  try {
    el.innerHTML = renderToStaticMarkup(<DeployScene stage="checking" outcome="running" />)
    expect(getComputedStyle(el.querySelector('.door')!).transform).not.toContain('scaleX')
    expect(el.querySelector('.door')?.getAttribute('data-door')).toBe('closed')
    el.innerHTML = renderToStaticMarkup(<DeployScene outcome="passed" />)
    expect(getComputedStyle(el.querySelector('.door')!).transform).toBe('scaleX(.25)')
  } finally { el.remove(); style.remove() }
})

it.each(Object.keys(deployWords) as DeployStage[])('keeps stage marks factual when %s is stopped or progress pauses', stage => {
  const el = document.createElement('div')
  const index = Object.keys(deployWords).indexOf(stage)
  for (const outcome of ['failed', 'paused'] as const) {
    el.innerHTML = renderToStaticMarkup(<DeployScene stage={stage} failedStage={stage} outcome={outcome} />)
    const marks = [...el.querySelectorAll('.deploy-stage-mark')].map(e => e.textContent)
    expect(marks).toEqual(Object.keys(deployWords).map((_, i) => i < index ? '✓' : i === index && outcome === 'failed' ? '✗' : '·'))
    expect(el.querySelector('[aria-label="running"]')).toBeNull()
  }
})

it('leaves the finished building lit and flagged after the crane and visitor finish their work', () => {
  const style = document.createElement('style')
  style.textContent = css.slice(css.indexOf('/* Deploy scenes'), css.indexOf('@keyframes deploy-fade'))
  document.head.append(style)
  const el = document.createElement('div'); document.body.append(el)
  try {
    el.innerHTML = renderToStaticMarkup(<DeployScene outcome="passed" />)
    for (const part of ['.blk', '.roof', '.win', '.door', '.pole', '.flag']) expect(getComputedStyle(el.querySelector(part)!).opacity).toBe('1')
    for (const part of ['.crane', '.visitor', '.spark']) expect(getComputedStyle(el.querySelector(part)!).opacity).toBe('0')
  } finally { el.remove(); style.remove() }
})

it('celebrates a reported live stage once and does not restart when the completed step arrives', () => {
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<DeployScene stage="checking" outcome="running" />))
    act(() => root.render(<DeployScene stage="live" outcome="running" />))
    expect(el.querySelector('.celebrate')).not.toBeNull()
    act(() => {
      const e = new Event('webkitAnimationEnd', { bubbles: true })
      Object.defineProperty(e, 'animationName', { value: 'deploy-burst' })
      el.querySelector('.spark')!.dispatchEvent(e)
    })
    act(() => root.render(<DeployScene outcome="passed" />))
    expect(el.querySelector('.celebrate')).toBeNull()
  } finally { act(() => root.unmount()) }
})

// The level jib and independently hoisted hook are the crane's visual contract.
it('keeps the jib still while trolleys carry and hoist each load', () => {
  const style = document.createElement('style')
  style.textContent = css.slice(css.indexOf('/* Deploy scenes'), css.indexOf('@keyframes deploy-fade'))
  document.head.append(style)
  const el = document.createElement('div'); document.body.append(el)
  try {
    el.innerHTML = renderToStaticMarkup(<DeployScene stage="building" outcome="running" />)
    expect(getComputedStyle(el.querySelector('.jib')!).animation).toBe('none')
    expect(el.querySelectorAll('.delivery .trolley')).toHaveLength(7)
    expect(el.querySelectorAll('.delivery .hoist')).toHaveLength(7)
    expect(el.querySelector('.jib .delivery')).toBeNull()
    for (const cable of el.querySelectorAll('.delivery .cable')) {
      expect(cable.getAttribute('x1')).toBe(cable.getAttribute('x2'))
    }
    expect(getComputedStyle(el.querySelector('.idle-rig')!).opacity).toBe('0')
    expect(getComputedStyle(el.querySelector('.active .hoist')!).animation).toContain('deploy-hoist')
  } finally { el.remove(); style.remove() }
})

it('uses the existing OK colour for the deployment card live heading', () => {
  const style = document.createElement('style')
  style.textContent = cardCss.replace('@layer pages {', '').replace(/}\s*$/, '')
  document.head.append(style)
  const el = document.createElement('div'); document.body.append(el)
  try {
    el.innerHTML = '<div class="deployment-deploy-card"><div class="ch-deploy-heading"><strong class="ok">It’s live!</strong></div></div>'
    const heading = getComputedStyle(el.querySelector('strong')!)
    expect(heading.fontSize).toBe('var(--fs-lg)')
    expect(heading.color).toBe('var(--ok)')
  } finally { el.remove(); style.remove() }
})

// Copying must keep the source lit and retain the destination in later frames.
it('carries a copy to a persistent shed without darkening the house', () => {
  const style = document.createElement('style')
  style.textContent = css.slice(css.indexOf('/* Deploy scenes'), css.indexOf('@keyframes deploy-copy'))
  document.head.append(style)
  const el = document.createElement('div'); document.body.append(el)
  try {
    for (const stage of ['backing-up', 'building', 'starting', 'checking', 'live'] as const) {
      el.innerHTML = renderToStaticMarkup(<DeployScene stage={stage} outcome="running" />)
      expect(getComputedStyle(el.querySelector('.backup-shed')!).opacity).toBe('1')
      expect(getComputedStyle(el.querySelector('.backup-box')!).opacity).toBe('1')
      if (stage === 'backing-up') {
        expect(getComputedStyle(el.querySelector('.win')!).opacity).toBe('1')
        expect(getComputedStyle(el.querySelector('.plan')!).opacity).toBe('0')
        expect(el.querySelector('.n1')).toBeNull()
        expect(getComputedStyle(el.querySelector('.backup-box')!).animation).toContain('deploy-copy')
      }
    }
    el.innerHTML = renderToStaticMarkup(<DeployScene outcome="passed" />)
    expect(getComputedStyle(el.querySelector('.backup-shed')!).opacity).toBe('1')
  } finally { el.remove(); style.remove() }
})

// The deployment card keeps every short stage visible, including the backup.
it('keeps compact backup progress and the shared spinner on deployment cards', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage="backing-up" outcome="running" compactStages />)
  const rows = [...el.querySelectorAll('.deploy-stages-compact li')]
  expect(rows.map(row => row.querySelector('span + span')?.textContent)).toEqual(['Code', 'Backup', 'Build', 'Start', 'Check', 'Live'])
  expect(rows[0].querySelector('.ok')?.textContent).toBe('✓')
  expect(rows[1].getAttribute('aria-current')).toBe('step')
  expect(rows[1].querySelector('.spinner[aria-label="running"]')).not.toBeNull()
  expect(el.querySelector('.backing-up.has-backup-shed')).not.toBeNull()
  el.innerHTML = renderToStaticMarkup(<DeployScene outcome="paused" compactStages />)
  expect(el.querySelectorAll('.deploy-stages-compact li')).toHaveLength(6)
  expect(el.querySelector('[aria-current], .spinner')).toBeNull()
})

// Pacing may estimate block placement, but only the server can finish the roof.
it('paces a 60 second build and restores the elapsed frame on a mid-build visit', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage="building" outcome="running" expectedBuildMs={60000} buildElapsedMs={15000} />)
  expect(el.querySelectorAll('.blk.placed')).toHaveLength(2)
  expect(el.querySelector('.roof.placed')).toBeNull()
  el.innerHTML = renderToStaticMarkup(<DeployScene stage="building" outcome="running" expectedBuildMs={60000} buildElapsedMs={90000} />)
  expect(el.querySelectorAll('.blk.placed')).toHaveLength(5)
  expect(el.querySelector('.delivery.holding .load rect')).not.toBeNull()
  expect(el.querySelector('.roof.placed')).toBeNull()
})

it('uses ten second deliveries without history', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage="building" outcome="running" buildElapsedMs={25000} />)
  expect(el.querySelectorAll('.blk.placed')).toHaveLength(3)
  expect(el.querySelector('.roof.placed')).toBeNull()
})


it('keeps the roof off through an overrun, then catches up in order when starting is reported', () => {
  vi.useFakeTimers()
  const el = document.createElement('div'), root = createRoot(el)
  const draw = (stage: DeployStage) => act(() => root.render(<DeployScene stage={stage} outcome="running" expectedBuildMs={60000} buildElapsedMs={15000} />))
  try {
    draw('building')
    expect(el.querySelectorAll('.blk.placed')).toHaveLength(2)
    act(() => vi.advanceTimersByTime(75000))
    expect(el.querySelectorAll('.blk.placed')).toHaveLength(5)
    expect(el.querySelector('.delivery.holding')).not.toBeNull()
    expect(el.querySelector('.roof.placed')).toBeNull()
    draw('starting')
    act(() => vi.advanceTimersByTime(150))
    expect(el.querySelectorAll('.blk.placed')).toHaveLength(6)
    expect(el.querySelector('.roof.placed')).toBeNull()
    act(() => vi.advanceTimersByTime(200))
    expect(el.querySelector('.roof.placed')).not.toBeNull()
  } finally { act(() => root.unmount()); vi.useRealTimers() }
})

it('finishes an unexpectedly short build within a moment and freezes unreadable building progress', () => {
  vi.useFakeTimers()
  const el = document.createElement('div'), root = createRoot(el)
  const draw = (stage: DeployStage, outcome: 'running' | 'paused') => act(() => root.render(<DeployScene stage={stage} outcome={outcome} expectedBuildMs={60000} buildElapsedMs={15000} />))
  try {
    draw('building', 'running')
    draw('building', 'paused')
    act(() => vi.advanceTimersByTime(60000))
    expect(el.querySelectorAll('.blk.placed')).toHaveLength(2)
    expect(el.querySelector('.roof.placed')).toBeNull()
    draw('starting', 'running')
    const counts = []
    for (let i = 0; i < 5; i++) {
      act(() => vi.advanceTimersByTime(180))
      counts.push(el.querySelectorAll('.blk.placed').length)
      if (i < 4) expect(el.querySelector('.roof.placed')).toBeNull()
    }
    expect(counts).toEqual([3, 4, 5, 6, 6])
    expect(el.querySelector('.roof.placed')).not.toBeNull()
  } finally { act(() => root.unmount()); vi.useRealTimers() }
})
