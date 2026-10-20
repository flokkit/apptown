// @vitest-environment jsdom
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { settled } from '../../settled'
import { cadenceWords, ScheduleRows } from './Schedules'
import type { ScheduleList } from '../../wire.gen'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0041-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// The reader is in Melbourne (UTC+11 in November): a schedule's words and
// times are theirs, not AppTown's UTC.
const env = (globalThis as unknown as { process: { env: Record<string, string | undefined> } }).process.env
const zone = env.TZ
beforeAll(() => { env.TZ = 'Australia/Melbourne' })
afterAll(() => { if (zone === undefined) delete env.TZ; else env.TZ = zone })
afterEach(() => { vi.unstubAllGlobals() })

it('says a cron in plain words at the reader\'s own time, and anything else as the cron', () => {
  expect(cadenceWords('0 6 1 * *', '2026-11-01T06:00:00Z')).toBe('monthly, 1st at 17:00')
  // 20:00 UTC on the 1st is the 2nd in Melbourne.
  expect(cadenceWords('0 20 1 * *', '2026-11-01T20:00:00Z')).toBe('monthly, 2nd at 07:00')
  expect(cadenceWords('30 22 * * *', '2026-11-01T22:30:00Z')).toBe('daily at 09:30')
  expect(cadenceWords('0 22 * * 0', '2026-11-01T22:00:00Z')).toBe('weekly, Monday at 09:00')
  expect(cadenceWords('15 * * * *', '2026-11-01T22:15:00Z')).toBe('hourly at :15')
  expect(cadenceWords('* * * * *', '2026-11-01T22:15:00Z')).toBe('every minute')
  expect(cadenceWords('0 6 1,15 * *', '2026-11-01T06:00:00Z')).toBe('cron 0 6 1,15 * * (UTC)')
  expect(cadenceWords('0 6 1 * *')).toBe('cron 0 6 1 * * (UTC)') // no known next firing
  // West of UTC the 1st at 06:00 UTC falls on the 31st, 30th or 28th: no
  // one local day is the cadence, so it is said in UTC.
  env.TZ = 'America/Los_Angeles'
  try {
    expect(cadenceWords('0 6 1 * *', '2026-11-01T06:00:00Z')).toBe('monthly, 1st at 06:00 UTC')
    expect(cadenceWords('0 20 15 * *', '2026-11-15T20:00:00Z')).toBe('monthly, 15th at 12:00')
  } finally { env.TZ = 'Australia/Melbourne' }
})

async function rows(answer: Response, then?: Response): Promise<{ text: string; html: string }> {
  let current = answer
  vi.stubGlobal('fetch', vi.fn(async () => current.clone()))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><ScheduleRows slug="p" /></MemoryRouter></QueryClientProvider>)
  })
  await settled(client)
  if (then) {
    current = then
    await act(async () => { await client.refetchQueries() })
    await settled(client)
  }
  const out = { text: el.textContent ?? '', html: el.innerHTML }
  act(() => root.unmount())
  client.clear()
  return out
}

const list = (l: ScheduleList) => new Response(JSON.stringify(l))
const schedule = { source: { workflow: 'dependency-uplift', version: 3 }, cron: '0 6 1 * *', timeZone: 'UTC', skipIfActive: true, due: false }

it('shows each schedule\'s cadence, next firing and last firing', async () => {
  const opened = await rows(list({ evalCopy: false, schedules: [{ ...schedule, next: '2026-11-01T06:00:00Z', last: { at: '2026-10-01T06:00:00Z', outcome: 'opened', changeId: CHANGE, changeNumber: 12 } }] }))
  expect(opened.text).toContain('dependency-uplift')
  expect(opened.text).toContain('monthly, 1st at 17:00')
  expect(opened.html).toContain('title="cron 0 6 1 * * (UTC)"')
  expect(opened.text).toContain('next: 1 Nov, 17:00')
  expect(opened.html).toContain(`href="/changes/${CHANGE}"`)
  expect(opened.text).toContain('1 Oct, 16:00 · change 12')

  const skipped = await rows(list({ evalCopy: false, schedules: [{ ...schedule, next: '2026-11-01T06:00:00Z', last: { at: '2026-10-01T06:00:00Z', outcome: 'skipped', changeNumber: 0 } }] }))
  expect(skipped.text).toContain('skipped: previous run still active')
  const failed = await rows(list({ evalCopy: false, schedules: [{ ...schedule, next: '2026-11-01T06:00:00Z', last: { at: '2026-10-01T06:00:00Z', outcome: 'failed', changeNumber: 0 } }] }))
  expect(failed.text).toContain('could not start')
  const never = await rows(list({ evalCopy: false, schedules: [{ ...schedule, next: '2026-11-01T06:00:00Z' }] }))
  expect(never.text).toContain('last: never')
  const due = await rows(list({ evalCopy: false, schedules: [{ ...schedule, next: '2026-10-01T06:00:00Z', due: true }] }))
  expect(due.text).toContain('due now')
})

it('says unknown when a schedule or the whole read failed, never none', async () => {
  const problem = await rows(list({ evalCopy: false, schedules: [{ ...schedule, source: { workflow: 'dependency-uplift', version: 0 }, problem: 'its workflow could not be read: boom' }] }))
  expect(problem.text).toContain('next: unknown')
  expect(problem.text).toContain('last: unknown')
  expect(problem.text).toContain('could not be read')
  const failed = await rows(new Response(JSON.stringify({ error: 'database down' }), { status: 500 }))
  expect(failed.text).toContain('could not be read')
  expect(await rows(list({ evalCopy: false, schedules: [] }))).toEqual({ text: '', html: '' })
  expect((await rows(list({ evalCopy: true, schedules: [] }))).text).toBe('Eval copy: runs no schedules.')
})

it('keeps an old none or eval copy marked out of date when its refresh fails', async () => {
  const down = new Response(JSON.stringify({ error: 'database down' }), { status: 422 }) // a 4xx is not retried
  const none = await rows(list({ evalCopy: false, schedules: [] }), down)
  expect(none.text).toContain('Schedules: none.')
  expect(none.text).toContain('out of date')
  const copy = await rows(list({ evalCopy: true, schedules: [] }), down.clone())
  expect(copy.text).toContain('Eval copy: runs no schedules.')
  expect(copy.text).toContain('out of date')
})

it('says it is reading before the first answer, never nothing', async () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><ScheduleRows slug="p" /></MemoryRouter></QueryClientProvider>)
  })
  expect(el.textContent).toBe('Reading the schedules…')
  act(() => root.unmount())
  client.clear()
})
