// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { TechnicalDetailSwitch } from './TechnicalDetail'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import type { RunTrace } from '../../wire.gen'
import WorkRecordPage, { logRows, workRecordHref } from './WorkRecord'
import { runStory } from './story'
import type { RunDetail } from '../../api'
import { ConversationLog } from './Conversation'

Element.prototype.scrollIntoView = vi.fn()
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const at = '2026-11-01T10:00:00Z'
const trace = () => ({ run: { id: 'run', workflowName: 'make', workflowVersion: 2, createdAt: at }, change: { id: 'change' }, stages: [], steps: [{ id: 'step', name: 'implement', type: 'agent/pi', stage: 'work', status: 'pass', config: {}, evidence: { model: 'test-model', durationMs: 90000, files: ['app.ts'], output: 'The report', reason: 'Needs a key.' }, startedAt: at, finishedAt: '2026-11-01T10:01:30Z' }], events: [], artifacts: [{ id: 'log', kind: 'agent-log', name: 'step-step', body: 'Checking the request\n→ read {"path":"app.ts"}\n→ edit {"path":"app.ts"}\n✔ 2 tool result(s)\n' + 'long output '.repeat(80), createdAt: at }, { id: 'instructions', name: 'steering-implement', kind: 'steering', body: 'Use the shared rules.', createdAt: at }], guidance: { imports: [{ name: 'rules/shared', version: 4 }] }, workflow: { yaml: 'name: library-copy', name: 'make', version: 2, definition: { kind: 'pipeline', stages: [] } } } as unknown as RunTrace)
const settle = () => act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
afterEach(() => vi.unstubAllGlobals())
async function mount(t = trace(), address = '/changes/change/work/step?run=run&attempt=step-0', conversation = false) {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const path = String(url).replace('/api/v1', '')
    const answers: Record<string, unknown> = {
      '/changes/change': { change: { id: 'change', number: 12, title: 'Fix the calendar', body: 'Keep the time.\nKeep the invitation.' }, projectSlug: 'club' },
      '/runs/run/trace': t, '/auth/me': { user: 'work-record-test' }, '/changes/change/worker-chats': { chats: [] },
      '/changes/change/transcript': { sessions: [] },
      '/changes/change/diff': 'diff --git a/app.ts b/app.ts\n--- a/app.ts\n+++ b/app.ts\n@@ -1 +1 @@\n-old\n+new',
      '/artifacts/log': { ...t.artifacts[0], body: 'Full retained message\n→ bash {"command":"test"}' },
    }
    return new Response(typeof answers[path] === 'string' ? answers[path] as string : JSON.stringify(answers[path] ?? {}))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  const rounds = runStory({ run: t.run, steps: t.steps, events: t.events, artifacts: t.artifacts } as RunDetail)
  await act(async () => root.render(<QueryClientProvider client={client}><TechnicalDetailSwitch /><MemoryRouter initialEntries={[address]}>{conversation ? <ConversationLog changeId="change" rounds={rounds} /> : <Routes><Route path="/changes/:id/work/:stepId/request" element={<WorkRecordPage section="request" />} /><Route path="/changes/:id/work/:stepId/workflow" element={<WorkRecordPage section="workflow" />} /><Route path="/changes/:id/work/:stepId/report" element={<WorkRecordPage section="report" />} /><Route path="/changes/:id/work/:stepId/records/:artifactId" element={<WorkRecordPage section="record" />} /><Route path="/changes/:id/work/:stepId" element={<WorkRecordPage />} /></Routes>}</MemoryRouter></QueryClientProvider>))
  await settle()
  await settle()
  return { el, close: () => { act(() => root.unmount()); el.remove(); client.clear() }, click: async (e: Element) => { await act(async () => (e as HTMLElement).click()); await settle() } }
}

it('gives the session its own address, named inputs, produced files and explicit data gaps', async () => {
  const f = await mount()
  for (const text of ['Work record', 'test-model', '90s', 'What the agent was given', 'The session', 'What it produced', 'Needs a key.', '+1 −1 (whole change)']) expect(f.el.textContent).toContain(text)
  // The version never sits on a line of its own: it is joined to the name.
  expect(f.el.querySelector('a[href="/guidance/rules%2Fshared?version=4"]')?.textContent).toBe('rules/shared\u00a0v4')
  expect(f.el.querySelector('a[href="/changes/change?tab=changes#diff-app.ts"]')).not.toBeNull()
  expect(f.el.querySelectorAll('.work-event')).toHaveLength(5)
  expect(f.el.querySelector('details, summary')).toBeNull()
  expect(f.el.textContent).not.toContain('Trace JSON')
  await f.click(f.el.querySelector<HTMLButtonElement>('button.ch-goggles')!)
  expect(f.el.textContent).toContain('Trace JSON')
  await f.click(f.el.querySelector<HTMLButtonElement>('button.ch-goggles')!)
  const full = [...f.el.querySelectorAll('a')].find(a => a.textContent === 'full text')!
  await f.click(full)
  // The heading names the kind of record; its own name follows.
  expect(f.el.querySelector('h1')?.textContent).toBe('Agent log')
  expect(f.el.querySelector('.work-byline')?.textContent).toBe('step-step')
  expect(f.el.querySelector('.work-full pre')?.textContent).toBe(trace().artifacts[0].body)
  expect(f.el.querySelector('.work-events')).toBeNull()
  f.close()
})

it('keeps hundreds of retained events in order and switches the whole view to messages', async () => {
  const t = trace(); t.artifacts[0].body = Array.from({ length: 300 }, (_, i) => `→ bash {"command":"echo ${i}"}`).join('\n') + '\nFinished reading'
  const f = await mount(t)
  expect(f.el.querySelectorAll('.work-event')).toHaveLength(301)
  expect(f.el.querySelector('.work-mini')?.textContent).toContain('300 ran')
  await f.click([...f.el.querySelectorAll('button')].find(b => b.textContent === 'messages only')!)
  expect(f.el.querySelectorAll('.work-event')).toHaveLength(1)
  await f.click([...f.el.querySelectorAll('button')].find(b => b.textContent === 'everything')!)
  expect(f.el.querySelectorAll('.work-event')[299].textContent).toContain('echo 299')
  f.close()
})

it('reads the full retained log instead of treating the artifact preview as complete', async () => {
  const t = trace(); t.artifacts[0].body = 'preview'; t.artifacts[0].bodyBytes = 1000
  const f = await mount(t)
  expect(f.el.textContent).toContain('Full retained message')
  expect(f.el.textContent).not.toContain('preview')
  f.close()
})

it('keeps attempts distinct and refuses a trace belonging to another change', async () => {
  const t = trace()
  t.events = [{ id: 'first', kind: 'step.started', at, payload: { step: 'implement', sessionId: 'one' } }, { id: 'reset', kind: 'step.retried', at: '2026-11-01T10:02:00Z', payload: { step: 'implement', previousEvidence: { files: ['old.ts'] }, previousStatus: 'pass' } }, { id: 'second', kind: 'step.started', at: '2026-11-01T10:03:00Z', payload: { step: 'implement', sessionId: 'two' } }] as RunTrace['events']
  const d = { run: t.run, steps: t.steps, artifacts: t.artifacts, events: t.events } as RunDetail
  const entries = runStory(d)
  expect(workRecordHref('change', entries[0])).not.toBe(workRecordHref('change', entries[1]))
  const f = await mount(t, '/changes/change/work/step?run=run&attempt=step-first')
  expect(f.el.textContent).toContain('old.ts')
  expect(f.el.textContent).not.toContain('The report')
  f.close()
  t.change!.id = 'another'
  const other = await mount(t)
  expect(other.el.textContent).toContain('not in this change')
  expect(other.el.textContent).not.toContain('Checking the request')
  other.close()
})

it('links agent rounds from Conversation to their work record', async () => {
  const f = await mount(trace(), '/changes/change?tab=conversation', true)
  expect(f.el.querySelector('a[href="/changes/change/work/step?run=run&attempt=step-0"]')?.textContent).toContain('work record')
  f.close()
})

it('preserves log order and never assigns a shared tool-result line to an individual call', () => {
  expect(logRows('Words\n→ read {}\n→ bash {}\n✔ 2 tool result(s)').map(r => r.kind)).toEqual(['said', 'read', 'ran', 'status'])
})

it('uses quiet missing facts and never emits empty separators in a run record', async () => {
  const t = trace(); t.steps = []; t.artifacts = []; t.guidance = null
  const f = await mount(t, '/changes/change/work/run?run=run&attempt=work-run')
  expect(f.el.querySelector('.work-byline')?.textContent).not.toMatch(/unknown|unavailable/)
  expect(f.el.textContent).toContain('not recorded')
  // A run-level entry is the run's record: no agent, and no file count the
  // run did not record; the whole change's code is one link away.
  expect(f.el.querySelector('.work-byline')?.textContent).toMatch(/^Run · /)
  expect(f.el.textContent).not.toContain('Files changed')
  expect(f.el.textContent).not.toContain('— · code for the whole change')
  expect(f.el.querySelector('a[href="/changes/change?tab=changes"]')?.textContent).toBe('code for the whole change →')
  expect(f.el.textContent).toContain('make v2 · run workflow text')
  for (const text of ['tokens and cost', 'Session identity unavailable', 'The saved agent log', 'No instruction record exposed', 'Worker chats are recorded', 'result unknown', ' ·  · ']) expect(f.el.textContent).not.toContain(text)
  f.close()
})

it.each([
  ['request','Change request','Keep the time.\nKeep the invitation.'],
  ['workflow','Workflow','kind: pipeline'],
  ['report','Report','The report'],
  ['records/instructions','Agent instructions','Use the shared rules.'],
])('reads %s on its own page with one title', async (part,title,text) => {
  const f = await mount(trace(), `/changes/change/work/step/${part}?run=run&attempt=step-0`)
  try {
    expect([...f.el.querySelectorAll('h1')].map(h=>h.textContent)).toEqual([title])
    expect(f.el.textContent).toContain(text)
    expect(f.el.textContent).not.toContain('library-copy')
    expect(f.el.querySelector('.work-events, details')).toBeNull()
  } finally { f.close() }
})

// An empty record is said in plain text outside the content box; the request reads as prose.
it('says an empty report outside the box and renders the request as prose', async () => {
  const t = trace() as unknown as { steps: { evidence: { output?: string } }[] }
  t.steps[0].evidence.output = ''
  const f = await mount(t as unknown as RunTrace, '/changes/change/work/step/report?run=run&attempt=step-0')
  try {
    expect(f.el.querySelector('.work-full')).toBeNull()
    expect(f.el.querySelector('p.muted')?.textContent).toBe('No report was recorded.')
  } finally { f.close() }
  const r = await mount(trace(), '/changes/change/work/step/request?run=run&attempt=step-0')
  try {
    expect(r.el.querySelector('.work-full pre')).toBeNull()
    expect(r.el.querySelector('.work-full .md')?.textContent).toContain('Keep the time.')
  } finally { r.close() }
})
