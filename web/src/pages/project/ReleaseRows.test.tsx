// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { ReleaseRows } from './Overview'
import type { Release } from '../../api'

it('leads with the change and commit while keeping release notes out of the list', () => {
  const release = { number: 5, serving: true, deployment: 'main', deploymentLabel: 'Production', createdAt: '2026-10-31',
    notes: 'To satisfy it: provide the independent review report', changes: [{ id: 'c', number: 9, title: 'Keep the date' }], commitSha: 'abcdef1234567890' } as Release
  const html = renderToStaticMarkup(<MemoryRouter><ReleaseRows slug="club" releases={[release]} /></MemoryRouter>)
  expect(html).toContain('live: Production #5')
  expect(html).toContain('href="/projects/club/releases/5"')
  expect(html).not.toContain('<button')
  expect(html).not.toContain('To satisfy it')
  expect(html).toContain('#9 Keep the date')
  expect(html.indexOf('#9 Keep the date')).toBeLessThan(html.indexOf('abcdef123456'))
  expect(html.indexOf('abcdef123456')).toBeLessThan(html.lastIndexOf('Production'))
})

it('names the release live on each deployment, not only the newest one', () => {
  const staging = { number: 23, serving: true, deployment: 'staging', deploymentLabel: 'staging', createdAt: '2026-11-10' } as Release
  const production = { number: 16, serving: true, deployment: 'main', deploymentLabel: 'Production', createdAt: '2026-11-01' } as Release
  const html = renderToStaticMarkup(<MemoryRouter><ReleaseRows slug="club" releases={[staging, production]} /></MemoryRouter>)
  expect(html).toContain('Production #16')
  expect(html).toContain('staging #23')
  expect(html).not.toContain('live: #23')
})
