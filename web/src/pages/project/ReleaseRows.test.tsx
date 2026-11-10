// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { ReleaseRows } from './Overview'
import type { Release } from '../../api'

it('keeps release notes and change narratives out of the overview release list', () => {
  const release = { number: 5, serving: true, deployment: 'main', deploymentLabel: 'Production', createdAt: '2026-10-31',
    notes: 'To satisfy it: provide the independent review report', changes: [{ title: 'A check narrative' }] } as Release
  const html = renderToStaticMarkup(<MemoryRouter><ReleaseRows slug="club" releases={[release]} /></MemoryRouter>)
  expect(html).toContain('live: Production #5')
  expect(html).toContain('href="/projects/club/releases/5"')
  expect(html).not.toContain('<button')
  expect(html).not.toContain('To satisfy it')
  expect(html).not.toContain('A check narrative')
})

it('names the release live on each deployment, not only the newest one', () => {
  const staging = { number: 23, serving: true, deployment: 'staging', deploymentLabel: 'staging', createdAt: '2026-11-10' } as Release
  const production = { number: 16, serving: true, deployment: 'main', deploymentLabel: 'Production', createdAt: '2026-11-01' } as Release
  const html = renderToStaticMarkup(<MemoryRouter><ReleaseRows slug="club" releases={[staging, production]} /></MemoryRouter>)
  expect(html).toContain('Production #16')
  expect(html).toContain('staging #23')
  expect(html).not.toContain('live: #23')
})
