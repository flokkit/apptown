// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { ReleaseRows } from './Overview'
import type { Release } from '../../api'

it('keeps release notes and change narratives out of the overview release list', () => {
  const release = { number: 5, serving: true, deployment: 'main', createdAt: '2026-10-31',
    notes: 'To satisfy it: provide the independent review report', changes: [{ title: 'A check narrative' }] } as Release
  const html = renderToStaticMarkup(<MemoryRouter><ReleaseRows slug="club" releases={[release]} /></MemoryRouter>)
  expect(html).toContain('live: #5')
  expect(html).toContain('▶ #5')
  expect(html).not.toContain('To satisfy it')
  expect(html).not.toContain('A check narrative')
})
