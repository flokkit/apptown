// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { HomeStatus, homeFinished, homeStatusParts, type Activity } from './Dashboard'

import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'

type Dep = Activity['deployments'][number]
const dep = (project: string, over: Partial<Dep> = {}): Dep => ({
  status: { word: 'working', reason: '', stale: false }, projectStatus: { word: 'working', reason: '', stale: false }, kind: 'standing', project, name: 'main', url: 'http://x', running: true,
  observation: 'running', targetName: 'Club host', observedAt: '2026-10-03T00:00:00Z', observationStale: false, ...over,
})
const empty = { needsYou: 0, running: 0, deployments: [], operations: { count: 0, known: true } }

describe('the home status strip', () => {
  it.each([true, false])('labels work and problems with their destinations, never a could-not-read count (known: %s)', known => {
    expect(homeStatusParts({ ...empty, needsYou: 3, running: 2, operations: { count: 8, unknown: 4, known } })).toEqual([
      { label: 'Waiting on you', value: 3, to: '#waiting-on-you' },
      { label: 'Other work', value: 2, to: '#other-work' },
      { label: 'Problems', value: 8, to: '/attention' },
    ])
  })
  it('omits zero counts when everything is read and empty', () => {
    expect(homeStatusParts(empty)).toEqual([])
  })
  it('never turns an unread needs-you count into zero', () => {
    expect(homeStatusParts({ ...empty, needsYou: null })).toEqual([
      { label: 'Waiting on you', value: "couldn't read", to: '#waiting-on-you' },
    ])
  })
  it('counts each live or unchecked project once and lets live take precedence', () => {
    expect(homeStatusParts({ ...empty, deployments: [
      dep('a'), dep('a', { name: 'staging' }), dep('b'), dep('b'),
      dep('c', { projectStatus: { word: 'unknown', reason: 'old reading', stale: true }, running: null }), dep('c', { name: 'staging', projectStatus: { word: 'unknown', reason: 'storage did not answer in time', stale: true }, running: null }),
      dep('d', { projectStatus: { word: 'unknown', reason: 'storage did not answer in time', stale: true }, running: null }),
      dep('e', { projectStatus: { word: 'stopped', reason: '', stale: false }, running: false }),
    ] })).toEqual([
      { label: 'Apps live', value: 2, to: '/deployments' },
      { label: 'Apps not checked', value: 2, to: '/deployments' },
    ])
  })
  it('shows zero apps live when deployments exist but none were seen running', () => {
    expect(homeStatusParts({ ...empty, deployments: [dep('a', { projectStatus: { word: 'stopped', reason: '', stale: false }, running: false })] })).toEqual([
      { label: 'Apps live', value: 0, to: '/deployments' },
    ])
  })
})

function strip(input: Parameters<typeof HomeStatus>[0]) {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(createElement(MemoryRouter, null, createElement(HomeStatus, input)))
  return el
}

it('renders every label above its value inside the matching link', () => {
  const el = strip({ ...empty, needsYou: 3, running: 2, deployments: [dep('live'), dep('unknown', { projectStatus: { word: 'unknown', reason: 'storage did not answer in time', stale: true }, running: null })], operations: { count: 8, unknown: 4, known: false } })
  expect([...el.querySelectorAll('.home-stat')].map(a => ({
    label: a.children[0]?.textContent, value: a.children[1]?.textContent, to: a.getAttribute('href'),
  }))).toEqual([
    { label: 'Waiting on you', value: '3', to: '#waiting-on-you' },
    { label: 'Other work', value: '2', to: '#other-work' },
    { label: 'Apps live', value: '1', to: '/deployments' },
    { label: 'Apps not checked', value: '1', to: '/deployments' },
    { label: 'Problems', value: '8', to: '/attention' },
  ])
})

it('renders an unread waiting count as words rather than zero', () => {
  const el = strip({ ...empty, needsYou: null })
  expect(el.querySelector('.home-stat .muted')?.textContent).toBe('Waiting on you')
  expect(el.querySelector('.home-stat-value')?.textContent).toBe("couldn't read")
  expect(el.querySelector('a')?.getAttribute('href')).toBe('#waiting-on-you')
  expect(el.textContent).not.toContain('All quiet')
})

it('replaces empty labelled counts with All quiet only when every read is known and empty', () => {
  const el = strip(empty)
  expect(el.textContent).toBe('All quiet')
  expect(el.querySelector('a')).toBeNull()
  for (const input of [
    { ...empty, needsYou: null },
    { ...empty, running: 1 },
    { ...empty, operations: { count: null, known: false } },
    { ...empty, operations: { count: 0, known: false } },
    { ...empty, operations: { count: 0, unknown: 1, known: false } },
    { ...empty, deployments: [dep('unknown', { projectStatus: { word: 'unknown', reason: 'storage did not answer in time', stale: true }, running: null })] },
  ]) expect(strip(input).textContent).not.toContain('All quiet')
})

// The feed is what changed: landed only; a failed change that needs
// something is already waiting on someone.
it('keeps the activity feed to landed changes', () => {
  const history = ['landed', 'failed', 'stopped', 'finished', 'unknown'].map(statusLabel => ({
    statusLabel, title: statusLabel === 'stopped' ? 'Release check: a chat answers' : statusLabel,
  })) as Activity['history']
  expect(homeFinished(history).map(c => c.statusLabel)).toEqual(['landed'])
})
