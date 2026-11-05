// @vitest-environment jsdom
import css from './deploy-scene.css?inline'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { DeployScene, DeploySceneScope, deployWords } from './DeployScene'
import type { DeployStage } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
it.each(Object.keys(deployWords) as DeployStage[])('draws only the reported %s frame and names it in words', stage => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<DeployScene stage={stage} outcome="running" />)
  const index = Object.keys(deployWords).indexOf(stage) + 1
  expect(el.querySelector('.deploy-scene')?.classList.contains(`g${index}`)).toBe(true)
  expect(el.querySelector('.deploy-scene')?.classList.contains(`g${index + 1}`)).toBe(false)
  expect(el.querySelector('[role="img"]')?.getAttribute('aria-label')).toContain(deployWords[stage])
  expect(el.querySelector('.celebrate')).toBeNull()
  expect(el.querySelector('[aria-current="step"]')?.textContent).toContain(deployWords[stage])
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

it('shares one celebration between the page and dialog, including closing it mid-animation', () => {
  const el = document.createElement('div'), root = createRoot(el)
  const draw = (passed: boolean, open = true) => act(() => root.render(<DeploySceneScope>
    <div hidden={open} data-page><DeployScene stage={passed ? 'live' : 'checking'} outcome={passed ? 'passed' : 'running'} /></div>
    <div hidden={!open} data-dialog><DeployScene stage={passed ? 'live' : 'checking'} outcome={passed ? 'passed' : 'running'} /></div>
  </DeploySceneScope>))
  try {
    draw(false)
    draw(true)
    act(() => {
      const e = new Event('webkitAnimationStart', { bubbles: true })
      Object.defineProperty(e, 'animationName', { value: 'deploy-raise' })
      el.querySelector('[data-dialog] .flag')!.dispatchEvent(e)
    })
    expect(el.querySelector('[data-dialog] .celebrate')).not.toBeNull()
    expect(el.querySelector('[data-page] .celebrate')).toBeNull()
    draw(true, false)
    expect(el.querySelector('[data-page] .celebrate')).toBeNull()
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

it.each(['starting', 'checking', 'live'] as const)('lights the windows only after starting completes (%s)', stage => {
  const style = document.createElement('style')
  style.textContent = css.slice(css.indexOf('/* Deploy scenes'), css.indexOf('@keyframes deploy-fade'))
  document.head.append(style)
  const el = document.createElement('div')
  document.body.append(el)
  try {
    for (const outcome of ['running', 'failed', 'passed'] as const) {
      if (outcome === 'passed' && stage !== 'live') continue
      el.innerHTML = renderToStaticMarkup(<DeployScene stage={stage} failedStage={stage} outcome={outcome} />)
      expect(getComputedStyle(el.querySelector('.win')!).opacity).toBe(stage === 'starting' ? '0' : '1')
    }
  } finally { el.remove(); style.remove() }
})
