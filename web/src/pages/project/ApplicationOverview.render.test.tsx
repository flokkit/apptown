// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import { StaleScope } from '../../ReadGate'
import { ApplicationOverview } from './ApplicationOverview'
import { fmtWhen } from '../../format'
import type { ProjectDetail } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const empty = { active: [], open: [], history: [] }

it.each(['open', 'failed', 'stale-empty'])('keeps current work before deployment and history, with honest %s reads', async mode => {
  let failed = mode === 'failed'
  const data = mode === 'open' ? { ...empty, active: [{ changeId: 'first', changeNumber: 1, title: 'First build', startedAt: '2026-11-01T12:00:00Z', waitingOn: { kind: 'work', says: 'building', tone: 'busy', who: [] } }], history: [{ changeId: 'first', title: 'Duplicate' }, ...Array.from({ length: 12 }, (_, i) => ({ changeId: `past-${i}`, changeNumber: i + 2, title: `Past change ${i}`, when: '2026-10-01', waitingOn: { kind: 'done', says: 'landed', tone: 'ok', who: [] } }))] } : empty
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url.endsWith('/overview')) return failed ? Response.json({ error: 'Activity unavailable' }, { status: 400 }) : Response.json(data)
    if (url.endsWith('/features')) return Response.json([{ area: 'backups', enabled: true }])
    if (url.endsWith('/deployments')) return Response.json(mode === 'open' ? [{ id: 'main', name: 'main', running: true, url: 'http://localhost:8000', hostOnly: true, targetName: 'Docker host', observation: 'running', status: { word: 'running', reason: '' } }] : [])
    if (url.endsWith('/backups')) return Response.json({ backups: [{ id: 'backup', deployment: 'main', takenAt: '2026-11-01', statusLabel: 'finished' }] })
    if (url.endsWith('/releases')) return Response.json(mode === 'open' ? [{ number: 1, deployment: 'main', statusLabel: 'deployed', createdAt: '2026-11-01', changes: [] }] : [])
    if (url.endsWith('/schedules')) return Response.json({ schedules: [{ name: 'Monthly updates', source: { workflow: 'update', version: 1 }, cron: '0 1 1 * *', next: '2026-12-01T01:00:00Z' }] })
    return Response.json([])
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><StaleScope><ApplicationOverview project={{ slug: 'demo', name: 'Demo' } as ProjectDetail} /></StaleScope></MemoryRouter></QueryClientProvider>))
    if (mode === 'open') {
      await vi.waitFor(() => expect(el.textContent).toContain('First build'))
      expect([...el.querySelectorAll('section')].map(s => s.getAttribute('aria-label'))).toEqual(['Open changes', 'Deployments', 'Activity', 'Backups', 'Schedules', 'About'])
      const current = el.querySelector('section[aria-label="Open changes"]')!
      expect(current.querySelector('a')?.getAttribute('href')).toBe('/changes/first')
      expect(current.textContent).toContain('#1 First build')
      expect(current.textContent).toContain('building')
      expect(current.textContent).toContain(fmtWhen('2026-11-01T12:00:00Z'))
      expect(el.querySelectorAll('a[href="/changes/first"]')).toHaveLength(1)
      expect(el.textContent).not.toContain('No activity yet')
      const activity = el.querySelector('section[aria-label="Activity"]')!
      expect(activity.textContent).toContain('Release 1 to main')
      expect(activity.querySelectorAll('.application-overview-card')).toHaveLength(10)
      await act(async () => [...activity.querySelectorAll('button')].find(b => b.textContent === 'Show 10 more')!.click())
      expect(activity.querySelectorAll('.application-overview-card')).toHaveLength(13)
      expect(el.querySelector('header')?.textContent).toContain('localhost:8000')
      expect(el.querySelector('section[aria-label="Deployments"] a[href="/projects/demo/deployments/main"]')).not.toBeNull()
      expect(el.querySelector('section[aria-label="Backups"]')?.textContent).toContain('main')
    } else if (mode === 'failed') {
      await vi.waitFor(() => expect(el.querySelector('section[aria-label="Activity"] .error')).not.toBeNull())
      expect(el.textContent).not.toContain('No activity yet')
      expect(el.querySelector('section[aria-label="Open changes"]')).toBeNull()
    } else {
      await vi.waitFor(() => expect(el.textContent).toContain('No activity yet'))
      failed = true
      await act(async () => { await client.invalidateQueries({ queryKey: ['get', 'projects', 'demo', 'overview'] }) })
      await vi.waitFor(() => expect(el.textContent).toContain('Refresh failed. Last answer shown.'))
      expect(el.querySelector('section[aria-label="Activity"] .stale-note')).not.toBeNull()
    }
  } finally { await act(async () => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
