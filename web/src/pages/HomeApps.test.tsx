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
// jsdom does not parse cascade layers; inspect the owning component rules.
const css = ['list-row', 'projects'].map(name =>
  readFileSync(`src/styles/${name}.css`, 'utf8').replace(/^@layer components \{/, '').replace(/\}\s*$/, ''),
).join('\n')

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
    '/dashboard': { endedOperations: [] },
    // The previous request is seeded so regressions fail on rendered behaviour.
    '/projects': projects,
    '/projects?limit=5&order=mine': { projects, total: projects.length },
  }
  for (const [path, answer] of Object.entries(data)) client.setQueryData(getKey(path), answer)
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><Dashboard /></QueryClientProvider></MemoryRouter>)
  client.clear()
  return el
}

// "Where's my thing": Home asks for the person's own five most recently
// worked-on projects and shows them in the order AppTown gave, running or
// not, with All projects (and how many there are) for the rest.
it('shows the recent projects in the order given, with All projects for the rest', () => {
  const projects = [
    project('draft', { hasDeployments: false, prodRunning: false, productionUrl: '' }),
    ...Array.from({ length: 4 }, (_, i) => project(`app-${i}`, { blurb: i % 2 ? '' : 'Read together' })),
  ]
  const el = home(projects)
  const rows = [...el.querySelectorAll('.project-row')]
  expect(rows.map(r => r.querySelector('.project-name')?.textContent)).toEqual(['draft', 'app-0', 'app-1', 'app-2', 'app-3'])
  expect(el.querySelector('section[aria-label="Recent projects"]')).not.toBeNull()
  expect(el.querySelector('a[href="/projects"]')?.textContent).toBe(`All projects (${projects.length}) →`)
  const actions = [...el.querySelectorAll('a')].filter(a => a.textContent === 'Open app ↗')
  expect(actions).toHaveLength(4)
  for (const [i, action] of actions.entries()) {
    expect(action.getAttribute('aria-label')).toBe(`Open app: app-${i} (opens in a new tab)`)
    expect(action.getAttribute('target')).toBe('_blank')
  }
  // Create project looks as it does on the Projects page.
  expect(el.querySelector('a[href="/projects/_new"]')?.textContent).toBe('+ Create project')
  expect(el.querySelector('.dash-head button')?.classList.contains('primary')).toBe(true)
})

// An app not yet read (just after a restart) keeps its link; only one seen stopped loses it.
it('offers Open app unless the app was seen stopped', () => {
  const el = home([project('unread', { prodRunning: null }), project('stopped', { prodRunning: false })])
  const opens = [...el.querySelectorAll('a')].filter(a => a.textContent === 'Open app ↗').map(a => a.getAttribute('aria-label'))
  expect(opens).toEqual(['Open app: unread (opens in a new tab)'])
})

it('keeps identity, state and action in three fixed row cells regardless of description', () => {
  const el = home([project('plain'), project('described', { blurb: 'Read together' })])
  const rows = [...el.querySelectorAll('.project-row')]
  expect(rows).toHaveLength(2)
  for (const row of rows) {
    expect([...row.children].map(c => c.className)).toEqual(['list-identity', 'list-state', 'list-action'])
    expect(row.querySelector('.project-action a')).not.toBeNull()
  }
  expect(rows[0].querySelector('.project-description')).toBeNull()
})

it('makes creation primary only for a confirmed empty Home', () => {
  const el = home([])
  const buttons = [...el.querySelectorAll('button')].filter(b => b.textContent === '+ Create project')
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
  expect(phone.find(r => r.selectorText === '.list-row')?.style.getPropertyValue('grid-template-columns')).toBe('minmax(0, 1fr)')
  expect(css).toContain('min-height: 44px')
  style.remove()
})

it('shows a checked age only for stale project observations, without an empty spacer', () => {
  const observedAt = new Date().toISOString()
  const el = home([project('fresh', { observedAt }), project('stale', { observedAt, observationStale: true })])
  const [fresh, stale] = [...el.querySelectorAll('.project-state')]
  expect(fresh.textContent).toContain('working')
  expect([...fresh.children].filter(c => !c.classList.contains('status-help')).map(c => c.textContent).join('')).not.toContain('Checked')
  expect(fresh.querySelector(':scope > .muted.small')).toBeNull()
  expect(stale.textContent).toContain('Checked')
})
