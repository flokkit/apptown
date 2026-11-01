// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import Dashboard from './Dashboard'
import { ProjectListRow, projectHue } from '../ProjectListRow'
import type { ProjectRow } from '../wire.gen'
// @ts-expect-error -- only the stylesheet read needs Node.
import { readFileSync } from 'node:fs'
const css = readFileSync('src/styles.css', 'utf8')

const project = (slug: string, over: Partial<ProjectRow> = {}): ProjectRow => ({
  id: slug, slug, name: slug, icon: '', blurb: '', lastRunAt: '',
  hasDeployments: true, prodRunning: true, observation: 'running', observationStale: false,
  productionUrl: `https://${slug}.example.test`, ...over,
}) as ProjectRow

function home(projects: ProjectRow[], activity: unknown = { active: [], agents: [], history: [], deployments: [] }) {
  const client = newQueryClient()
  const data = {
    '/auth/me': { mode: 'internal', user: 'reviewer' },
    '/attention': { items: [], sources: [{ key: 'workers', unknown: false, stale: false }] },
    '/activity': activity,
    '/dashboard': { pendingApprovals: [], endedOperations: [] },
    // The previous request is seeded so regressions fail on rendered behaviour.
    '/projects': projects,
    '/projects?limit=50&order=running': { projects, total: projects.length },
  }
  for (const [path, answer] of Object.entries(data)) client.setQueryData(getKey(path), answer)
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><Dashboard /></QueryClientProvider></MemoryRouter>)
  client.clear()
  return el
}

it('offers every running app in the bounded page before non-running projects', () => {
  const projects = [project('stopped', { prodRunning: false, observation: 'stopped' }),
    ...Array.from({ length: 9 }, (_, i) => project(`app-${i}`, { blurb: i % 2 ? '' : 'Read together' })),
    project('stale', { observationStale: true, prodRunning: null }), project('unknown', { prodRunning: null, observation: 'unknown' })]
  const el = home(projects)
  const actions = [...el.querySelectorAll('a')].filter(a => a.textContent === 'Open app ↗')
  expect(actions).toHaveLength(9)
  expect(actions.every(a => a.classList.contains('primary'))).toBe(true)
  for (const [i, action] of actions.entries()) {
    expect(action.getAttribute('aria-label')).toBe(`Open app: app-${i} (opens in a new tab)`)
    expect(action.querySelector('[aria-hidden="true"]')?.textContent).toBe('↗')
    expect(action.getAttribute('target')).toBe('_blank')
  }
  expect(el.querySelector('a[href="/projects"]')?.textContent).toBe('All projects →')
  const rows = [...el.querySelectorAll('.project-row')]
  expect(rows).toHaveLength(12)
  expect(rows[0].textContent).toContain('app-0')
  expect(rows[9].textContent).toContain('stopped')
  expect(el.querySelectorAll('.project-description')).toHaveLength(5)
  expect(el.querySelector('.dash-head button')?.classList.contains('primary')).toBe(false)
})

it('keeps identity, state and action in three fixed row cells regardless of description', () => {
  const el = home([project('plain'), project('described', { blurb: 'Read together' })])
  const rows = [...el.querySelectorAll('.project-row')]
  expect(rows).toHaveLength(2)
  for (const row of rows) {
    expect([...row.children].map(c => c.className)).toEqual(['project-identity', 'project-state', 'project-action'])
    expect(row.querySelector('.project-action a')).not.toBeNull()
  }
  expect(rows[0].querySelector('.project-description')).toBeNull()
})

it('makes creation primary only for a confirmed empty Home', () => {
  const el = home([])
  const buttons = [...el.querySelectorAll('button')].filter(b => b.textContent === '+ New project')
  expect(buttons).toHaveLength(1)
  expect(buttons[0].classList.contains('primary')).toBe(true)
  expect(buttons[0].classList.contains('btn-big')).toBe(true)
})

it('opens running production even when a preview has an urgent aggregate status', () => {
  const el = home([project('production', { observation: 'unreachable', observationStale: true })])
  expect(el.querySelector('.project-action a')?.getAttribute('href')).toBe('https://production.example.test')
  expect(el.querySelector('.project-state')?.textContent).toContain('unreachable')
})

it('marks a failed project refresh unknown and removes the app action', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><ProjectListRow project={project('app')} refreshFailed reason="Connection lost" /></MemoryRouter>)
  expect(el.querySelector('.project-action a')).toBeNull()
  expect(el.querySelector('.project-state')?.textContent).toContain('unknown')
  expect(el.textContent).toContain('Connection lost')
})

it('gives letter avatars a stable slug colour in the shared row', () => {
  expect(projectHue('library')).toBe(projectHue('library'))
  expect(projectHue('library')).not.toBe(projectHue('calendar'))
  const el = home([project('library'), project('chosen', { icon: '⛵' })])
  expect(el.querySelector('.project-letter')?.getAttribute('style')).toContain(`--project-hue:${projectHue('library')}`)
  expect(el.querySelectorAll('.project-letter')).toHaveLength(1)
})

it('keeps apps available without claiming all quiet while activity is unread', () => {
  const el = home([project('library')], null)
  expect(el.querySelector('.project-action a')?.getAttribute('href')).toBe('https://library.example.test')
  expect(el.querySelector('.dash-lead')).toBeNull()
})

it('keeps phone actions and purpose visible in compact rows with tap-sized actions', () => {
  // jsdom parses media rules but does not apply them to markup.
  const style = document.createElement('style')
  style.textContent = css
  document.head.append(style)
  const rules = [...style.sheet!.cssRules].filter((r): r is CSSMediaRule => r instanceof CSSMediaRule && r.conditionText.includes('max-width'))
  for (const media of rules) for (const rule of [...media.cssRules]) {
    if (!(rule instanceof CSSStyleRule)) continue
    for (const selector of rule.selectorText.split(',')) {
      if (/project-action|project-description/.test(selector) && selector.trim() !== '.project-action:empty') {
        expect(rule.style.display, selector).not.toBe('none')
      }
    }
  }
  const phone = rules.flatMap(r => [...r.cssRules]).filter((r): r is CSSStyleRule => r instanceof CSSStyleRule)
  expect(phone.find(r => r.selectorText === '.project-action')?.style.getPropertyValue('grid-column')).toBe('4')
  expect(phone.find(r => r.selectorText === '.project-action .btn')?.style.getPropertyValue('min-height')).toBe('44px')
  style.remove()
})
