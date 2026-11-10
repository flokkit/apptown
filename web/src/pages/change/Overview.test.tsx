import { withFeatures, enabledFeatures } from '../../features.fixture'
// @vitest-environment jsdom
import DiffView from '../../DiffView'
import { Screenshots } from '../../Screenshots'
import { Status } from '../../Status'
import { InlineHelp } from '../../InlineHelp'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import type { ChangeView, Step } from '../../api'
import ChangePage from '../ChangePage'
import { CheckSummary, ChangeOverview, approvalBrief, checkAttention, progressTitle } from './Overview'
import { Evidence } from './Evidence'
import { nextStep, checksRunning } from './next'
import { TurnDecision, viewerTurn } from './Turn'
import { DecisionChoices } from '../../DecisionChoices'
import { heldLabels } from './HeldDecision'
import { StatePanel } from './StatePanel'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
class Observer { observe() {} disconnect() {} }
vi.stubGlobal('IntersectionObserver', Observer)
vi.stubGlobal('ResizeObserver', Observer)
HTMLDialogElement.prototype.showModal = function () { this.open = true }
HTMLDialogElement.prototype.close = function () { if (this.open) { this.open = false; this.dispatchEvent(new Event('close')) } }
const id = '00000000-0009-7000-8000-000000000000'
const step = (type: Step['type'], status = 'pass', evidence = {}, extra = {}) => ({ id: type, runId: 'run', stage: 'check', name: type, type, status, statusLabel: status === 'pass' ? 'passed' : status, isCheck: ['script','tests','scan','guidance/conformance','agent/review','scm/merged'].includes(type), checkAttention: type !== 'approval' && (!['pass','not_applicable'].includes(status) || type === 'tests'), config: {}, evidence, overrideBy: '', overrideReason: '', ...extra } as Step)
const approval = step('approval', 'pending', {}, { config: { means: 'I accept this work in full.', instructions: 'Read every check.' } })
const view = (): ChangeView => ({
  waitingOn: { kind: 'approval', says: 'ready for your approval', tone: 'warn', run: 'run', step: 'approval', checks: 0, who: [], you: 'yes' }, change: { id, number: 9, title: 'Winter rides', body: 'Show all the winter ride times.\nKeep the dinner invitation.', summary: 'Adds the winter ride calendar.', state: 'landing', createdAt: '2026-11-01T10:00:00Z', branch: 'change-9', baseSha: 'abc', editRevision: 0, openedBy: 'sam', type: 'feature' },
  checks: { tone: 'ok', text: 'All checks passed', started: true }, projectSlug: 'club', state: 'waiting on you', statusLabel: 'waiting for you',
  detail: { run: { id: 'run', number: 2, currentStage: 'check', status: 'running', statusLabel: 'working', workflowName: 'work', workflowVersion: 1, createdAt: '' },
    stages: ['check'], steps: [step('script'), step('agent/review', 'pass', { verdict: 'approve', summary: 'The calendar matches the request.' }), approval],
    next: [{ act: 'approve', stepId: approval.id }, { act: 'rework', stepId: 'agent' }], landingMeans: ['Merge and release the accepted work.'], events: [],
    artifacts: [{ id: 'record', runId: 'run', name: 'supporting report', kind: 'report', body: 'The complete report is retained.', ref: 'https://example.test/report', createdAt: '' }] },
  evidence: { targets: [{ title: 'Every ride has a start time', block: 'calendar', check: 'ride-times', expects: 'All times are shown.' }], merged: false, releases: [], mergeLabel: 'not merged', inAdvance: { approver: 'sam', at: '', shown: ['Acceptance recorded in advance.'] } },
  approval: { stepId: approval.id, viewer: 'yes' }, next: [{ act: 'abandon' }], runs: [], adrs: [], editing: 0, commits: [], diffstat: '5 files changed, 35 insertions(+), 15 deletions(-)', usage: { total: 1200, costMicros: 1000 }, committedDiff: { status: 'changed' },
} as unknown as ChangeView)
const featureClient = () => { const client = newQueryClient(); client.setQueryData(getKey('/features'), enabledFeatures); return client }
const render = (v: ChangeView) => renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={featureClient()}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>)
afterEach(() => { vi.unstubAllGlobals(); vi.stubGlobal('IntersectionObserver', Observer); vi.stubGlobal('ResizeObserver', Observer) })

it('leads with the preview, separates request from report and links every evidence row', () => {
  const html = render(view())
  const facts = ['The change', 'Preview', 'Open it and try it', 'You asked', 'What was made', 'Checks', 'Reviewer', 'Your decision']
  for (let i = 1; i < facts.length; i++) expect(html.indexOf(facts[i])).toBeGreaterThan(html.indexOf(facts[i - 1]))
  for (const fact of ['/records/checks', '/records/review', '?tab=changes', 'How it was done']) expect(html).toContain(fact)
  expect(html).not.toContain('Keep the dinner invitation.')
  expect(html).toContain('full request →')
  expect(html).not.toContain('No review recorded')
})

it('holds acceptance during a check, while keeping pending later-stage work separate', () => {
  const v = view()
  v.detail!.steps.push(step('scan', 'running')); v.waitingOn = { ...v.waitingOn, kind: 'work', says: 'working', you: 'no' }
  const el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.querySelector('[aria-label="Now"]')).not.toBeNull()
  expect(el.querySelector('#decision')?.hasAttribute('hidden')).toBe(true)
  expect(el.textContent).not.toContain('Checks are running; you can accept when they finish.')
  expect(render(v)).not.toContain('>Accept and land<')
  expect(checksRunning([step('scan', 'pending', {}, { stage: 'ship' })], 'check')).toBe(false)
  expect(checksRunning([step('scan', 'running', {}, { stage: 'ship' })], 'check')).toBe(true)
})

it('keeps unknown evidence, exceptions, skips, non-applicability and required approvals visible', () => {
  const steps = [step('scan', 'pass', {}, { checkAttention: true, checkReading: { unreadable: 'The scan could not be decoded.' } }), step('tests', 'pass'),
    step('script', 'fail', { reason: 'Exit 2' }), step('preview', 'not_applicable', { reason: 'No web part.' }),
    step('guidance/conformance', 'pass', {}, { checkAttention: true, checkReading: { checks: [{ check: 'privacy', verdict: 'not-applicable', detail: 'No personal data.' }], undecided: ['performance'] } }),
    step('scan', 'fail', {}, { id: 'exception', overrideBy: 'sam', overrideReason: 'Documented exception' }), approval]
  const html = renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={steps} />)
  for (const fact of ['The scan could not be decoded.', 'Tests: number unknown', 'Exit 2', 'performance: unknown', 'Documented exception']) expect(html).toContain(fact)
  expect(html).not.toContain('All checks passed')
})

it('opens Overview by default and keeps code, conversation, full records and editors reachable in four tabs', async () => {
  const scroll = vi.fn(); Element.prototype.scrollIntoView = scroll
  const v = view()
  v.notesDiff = { added: ['- Credit photos. (agent · change 9)'], removed: ['- Use illustrations. (Sam · change 4)'] }
  v.mainConflict = { mainBranch: 'main', step: 'implement', files: ['calendar.ts'], catchUp: 'git merge origin/main' } as ChangeView['mainConflict']
  v.detail!.artifacts[0].body = 'Report start.'
  v.detail!.artifacts[0].bodyBytes = 8000
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => {
    const path = String(url)
    const data = path.endsWith('/artifacts/record') ? { ...v.detail!.artifacts[0], body: 'The complete report is retained.' } : path.endsWith(`/changes/${id}`) ? v : path.endsWith('/worker-chats') ? { chats: [] } : path.endsWith('/transcript') ? { sessions: [], unreadable: 0 }
      : path.endsWith('/question') ? { adds: null } : path.endsWith('/preview') ? {} : path.endsWith('/auth/me') ? { user: 'sam' } : []
    return new Response(JSON.stringify(data))
  })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  const settle = () => vi.waitFor(async () => {
    await act(async () => { await new Promise(r => setTimeout(r, 0)) })
    expect(client.isFetching()).toBe(0)
  })
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/changes/${id}`]}><Routes><Route path="/changes/:id" element={<ChangePage />} /></Routes></MemoryRouter></QueryClientProvider>))
  await settle()
  await settle()
  expect([...el.querySelectorAll('.ch-tabs a')].map(a => a.textContent)).toEqual(['Overview', 'Code', 'Conversation', 'Details'])
  expect(el.querySelector('.ch-tabs [aria-current]')?.textContent).toBe('Overview')
  expect(el.querySelector('.ch-change-card')?.textContent).toContain('Project notes: adds 1, removes 1')
  expect(el.querySelector('.ch-change-card ins')?.textContent).toBe(v.notesDiff!.added[0])
  expect(el.querySelectorAll('.step-card')).toHaveLength(0)
  expect(el.querySelector('details')).toBeNull()
  expect(el.querySelector('.ch-state .ch-quiet')).toBeNull()
  expect(el.querySelector('.ch-state a')).toBeNull()
  expect(el.querySelector('#decision button')).toBeNull()
  expect(el.querySelector('.ch-turn-floater button')?.textContent).toBe('Decide…')
  expect(el.querySelector('.ch-overview')?.textContent).toContain('Changes to the app contract need checking.')
  expect(el.querySelector('.decision-form')?.textContent).not.toContain('Changes to the app contract need checking.')
  v.change.summary = ''
  await act(async () => { await client.invalidateQueries({ queryKey: getKey(`/changes/${id}`) }) })
  await settle()
  expect(vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith('/distill'))).toHaveLength(0)
  const click = async (name: string) => { await act(async () => [...el.querySelectorAll('.ch-tabs a')].find(a => a.textContent === name)!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))); await settle() }
  await act(async () => el.querySelector<HTMLButtonElement>('.ch-decide')!.click())
  const catchUp = [...el.querySelectorAll('dialog a')].find(a => a.textContent === 'Open Details for the catch-up command')!
  await act(async () => catchUp.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })))
  await settle()
  expect(el.querySelector('.ch-tabs [aria-current]')?.textContent).toBe('Details')
  expect(el.querySelector('.ch-details')?.textContent).toContain('git merge origin/main')
  expect(el.querySelectorAll('.step-card')).toHaveLength(0)
  expect(el.querySelectorAll('.ch-story-entry').length).toBeGreaterThan(2)
  expect(el.querySelectorAll('h1')).toHaveLength(1)
  for (const absent of ['No release is recorded', 'No attachments.', 'No merge is recorded', 'Runs and steps', 'Requirements and delivery', 'Try preview']) expect(el.querySelector('.ch-details')?.textContent).not.toContain(absent)
  expect(el.querySelector('details, summary')).toBeNull()
  expect(el.textContent).not.toContain('Other actions')
  for (const fact of ['by sam', 'Show all the winter ride times.', 'full request →', 'Branch', 'change-9', 'Started from', 'abc', 'Kind', 'Cost', 'Attachments', 'sam']) expect(el.textContent).toContain(fact)
  expect(el.querySelector('button.link')?.textContent).toBeTruthy()
  expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith('/artifacts/record'))).toBe(false)
  const session = [...el.querySelectorAll('a')].find(a => a.textContent === 'session')!
  expect(session.getAttribute('href')).toContain(`/changes/${id}/work/`)
  expect(session.getAttribute('href')).toContain('run=')
  expect(el.querySelector('a[href$="record-record"]')).toBeNull()
  await click('Overview')
  await click('Conversation')
  expect(el.textContent).toContain('Show all the winter ride times.')
  expect(el.textContent).not.toContain('Keep the dinner invitation.')
  expect(el.textContent).not.toContain('No chat yet.')
  await click('Code')
  expect(el.querySelector('.ch-tabs [aria-current]')?.textContent).toBe('Code')
  expect(el.querySelector('.ch-turn-floater button')?.textContent).toBe('Decide…')
  await act(async () => el.querySelector<HTMLButtonElement>('.ch-turn-floater button')!.click())
  await settle()
  expect(el.querySelector('.ch-tabs [aria-current]')?.textContent).toBe('Code')
  expect(el.querySelector('dialog[open] h2')?.textContent).toBe('Accept this change?')
  expect(el.querySelector('dialog[open]')?.textContent).toContain('Project notes: adds 1, removes 1')
  expect(el.querySelector('dialog[open] ins')?.textContent).toBe(v.notesDiff!.added[0])
  expect(el.querySelector('dialog[open] del')?.textContent).toBe(v.notesDiff!.removed[0])
  expect([...el.querySelectorAll('dialog .ch-decision-choices button')].map(b => b.textContent)).toEqual(['Accept', 'Send back…', 'Abandon change'])
  expect(el.querySelector('#decision button')).toBeNull()
  expect(el.querySelector('dialog a[href$="tab=conversation"]')).toBeNull()
  expect(vi.mocked(fetch).mock.calls.some(([url, init]) => init?.method === 'POST' && /approve|abandon|console/.test(String(url)))).toBe(false)
  for (const name of ['Overview', 'Conversation', 'Details', 'Code']) {
    await click(name)
    expect(el.querySelector('.ch-turn-floater button')?.textContent).toBe('Decide…')
    expect(el.querySelectorAll('.ch-turn-floater')).toHaveLength(1)
  }
  await act(async () => root.unmount()); el.remove(); client.clear()
})

it('never puts terminal output or the acceptance form in the top card', async () => {
  const v = view()
  v.detail!.steps[0] = step('agent/pi', 'running')
  v.waitingOn = { ...v.waitingOn, kind: 'work', says: 'working', tone: 'neutral', you: 'no' }; v.state = 'agent working'; v.statusLabel = 'working'; v.change.state = 'in_progress'
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).endsWith('/console') ? { sessions: [] } : String(url).endsWith('/deployments') ? [] : String(url).endsWith('/auth/me') ? { user: 'sam' } : v)))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/changes/${id}`]}><Routes><Route path="/changes/:id" element={<ChangePage />} /></Routes></MemoryRouter></QueryClientProvider>))
  await act(async () => { for (let i = 0; i < 5; i++) await new Promise(r => setTimeout(r, 0)) })
  expect(el.querySelector('.ch-state h1')?.textContent).toBe('Change #9: Winter rides')
  expect(el.querySelector('.ch-state .terminal')).toBeNull()
  expect(el.querySelector('.ch-state .decision-form')).toBeNull()
  expect(el.querySelector('.ch-state')?.textContent).toContain('working')
  expect(el.querySelector('.ch-decision')?.hasAttribute('hidden')).toBe(true)
  expect(el.querySelector('.ch-decision')?.textContent).not.toContain('Checks are running; you can accept when they finish.')
  await act(async () => root.unmount()); client.clear()
})

it('lets a called workflow describe its own waiting decision without saying the parent checks are running', () => {
  const v = view()
  v.detail!.steps = [step('workflow/call', 'running', { childRunId: 'child', workflow: 'calendar review' })]
  v.detail!.next = []
  const html = render(v)
  expect(html).toContain('Reading the called workflow’s checks…')
  expect(html).not.toContain('Checks are running; you can accept when they finish.')
})


it('omits technical check names and non-applicability and keeps approvals out of Checks', () => {
  const html = renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[
    step('scan', 'fail', {}, { name: 'scan-source:osv' }),
    step('scan', 'not_applicable', { reason: 'No dependencies.' }, { id: 'na', name: 'scan-source:osv' }), approval,
  ]} />)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.textContent).toContain('Dependency scan failed')
  expect(el.querySelector('.ch-technical')?.getAttribute('aria-hidden')).toBe('true')
  expect(el.textContent).not.toContain('required approval')
  expect(el.querySelector('details')).toBeNull()
  expect(el.textContent).not.toContain('No dependencies.')
})

it('shows the change report when the summary is Project work', () => {
  const v = view()
  v.change.summary = 'Project work'
  v.report = 'Added the winter ride calendar.\nAll six rides have start times.'
  const html = render(v)
  expect(html).toContain('Added the winter ride calendar.')
  expect(html).toContain('All six rides have start times.')
  expect(html).not.toContain('Project work')
  expect(html).toContain('5 files +35 −15')
  expect(html).toContain('Code: 5 files changed →')
  expect(html).not.toContain('How it was done → Details')
})

it('omits non-applicable checks without hiding failed or unknown checks', () => {
  const html = renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step('guidance/conformance', 'fail', {}, { checkAttention: true, checkReading: { checks: [
    { check: 'Privacy', verdict: 'not-applicable', detail: 'No personal data.' },
    { check: 'Accessibility', verdict: 'fail', detail: 'Missing labels.' },
  ], undecided: ['Performance'] } })]} />)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelector('details')).toBeNull()
  expect(el.querySelector('.ch-check-exception')?.textContent).toContain('Missing labels.')
  expect(el.querySelector('.ch-check-exception')?.textContent).toContain('Performance: unknown')
  expect(el.textContent).not.toContain('No personal data.')
})

it('shows a missing report honestly and uses the request once', () => {
  const v = view(); v.checks = { tone: 'neutral', text: 'Not checked yet. Checks run when you press Check and land in the chat.', started: false }; v.change.summary = ''; v.report = ''
  const el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.querySelector('.ch-request')?.textContent).toContain('Show all the winter ride times.')
  expect(el.textContent).toContain('Reading the conversation…')
  expect(el.querySelector('.ch-change-card')?.textContent).not.toContain('Keep the dinner invitation.')
  // Once where it can be seen: the hidden decision card is the Decide… screen's source.
  el.querySelectorAll('[hidden]').forEach(n => n.remove())
  expect(el.textContent?.split('Show all the winter ride times.').length).toBe(2)
  expect(el.textContent).not.toContain('No summary recorded')
  expect(el.textContent).not.toContain('The request:')
  expect(el.querySelector('details')).toBeNull()
})
it('shows a long request excerpt with a link to the complete request', () => {
  const v = view(); v.checks = { tone: 'neutral', text: 'Not checked yet. Checks run when you press Check and land in the chat.', started: false }; v.change.summary = ''; v.report = ''; v.change.body = 'First line.\nSecond line.\nThird line.\nFourth line.\nLast request line.'
  const el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.textContent).toContain('First line.')
  expect(el.querySelector('.ch-change-card')?.textContent).not.toContain('Last request line.')
  const link = [...el.querySelectorAll('a')].find(a => a.textContent === 'full request →')!
  expect(link.getAttribute('href')).toBe(`/changes/${v.change.id}/records/request`)
})

it('keeps large code files and screenshot failures open, and status details on the status, on a change', () => {
  const raw = 'diff --git a/site.txt b/site.txt\n--- a/site.txt\n+++ b/site.txt\n@@ -1 +1,450 @@\n' + Array.from({ length: 450 }, (_, i) => `+line ${i}`).join('\n')
  const html = renderToStaticMarkup(<InlineHelp.Provider value>
    <Status word="unknown" help="The checks could not be read." />
    <DiffView raw={raw} />
    <Screenshots shots={{ failedResources: [{ path: '/', view: 'desktop', type: 'image', url: '/missing.png', status: 404 }] } as never} />
  </InlineHelp.Provider>)
  const el = document.createElement('div'); el.innerHTML = html
  expect(el.querySelector('details, summary')).toBeNull()
  for (const text of ['line 449', '/missing.png', '404']) expect(el.textContent).toContain(text)
  // A status shows its word; its details open on hover or tap, not folded into a summary.
  expect(el.querySelector('.chip-button')?.getAttribute('data-help')).toBe('The checks could not be read.')
})

it('links a long reviewer conclusion to the run that recorded it', () => {
  const v = view()
  v.detail!.steps[1] = step('agent/review', 'pass', { summary: 'Review finding. '.repeat(80) }, { id: 'review', runId: 'working-run' })
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={featureClient()}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={(step, run) => `?tab=details&run=${run}&step=${step}`} /></QueryClientProvider></MemoryRouter>)
  const el = document.createElement('div'); el.innerHTML = html
  const link = [...el.querySelectorAll('a')].find(a => a.textContent === 'full review →')!
  expect(link.getAttribute('href')).toBe(`/changes/${v.change.id}/records/review?run=working-run`)
})

it.each([[true, true], [true, false], [false, true], [false, false]])('states acceptance consequences and only offered decisions (deploys=%s, abandon=%s)', async (deploys, abandon) => {
  const v = view()
  if (!abandon) v.next = []
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(
    String(url).endsWith('/deployments') ? deploys ? [{ feed: 'main', url: 'https://school.example.test' }] : []
      : String(url).endsWith('/question') ? { adds: null } : String(url).endsWith('/auth/me') ? { user: `consequence-${deploys}` } : {}
  )))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
  for (let j = 0; j < 3; j++) await act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
  expect(el.textContent).toContain('Landing puts the change on main. Deployments update separately.')
  const label = 'Accept'
  expect(el.querySelector('#decision button')).toBeNull()
  expect(el.querySelector<HTMLElement>('#decision')!.hidden).toBe(true)
  await act(async () => el.querySelector<HTMLButtonElement>('.ch-turn-floater button')!.click())
  expect(el.querySelector('dialog[open]')?.textContent).toContain('Landing puts the change on main. Deployments update separately.')
  expect(el.querySelector('dialog[open]')).not.toBeNull()
  expect(el.querySelector('dialog .decision-meaning')!.compareDocumentPosition(el.querySelector('dialog .ch-decision-choices')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(el.querySelector('dialog .decision-meaning')?.textContent).toContain('By accepting, you confirm: “I accept this work in full.”')
  expect([...el.querySelectorAll('dialog .ch-decision-choices button')].map(b => b.textContent)).toEqual([label, 'Send back…', ...(abandon ? ['Abandon change'] : [])])
  for (let i = 0; i < 8; i++) await act(async () => { await new Promise(r => setTimeout(r, 0)) })
  expect(el.querySelector<HTMLButtonElement>('dialog .primary')!.disabled).toBe(false)
  expect(el.querySelector('dialog textarea, dialog input')).toBeNull()
  const click = async (text: string) => act(async () => [...el.querySelectorAll<HTMLButtonElement>('dialog button')].find(b => b.textContent === text)!.click())
  await click('Send back…')
  await act(async () => {
    const field = el.querySelector('textarea')!
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(field, 'Change the time')
    field.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await click('Cancel')
  el.querySelector<HTMLElement>('dialog .decision-meaning')!.getBoundingClientRect = () => ({ top: 100, bottom: 150 }) as DOMRect
  await click(label)
  const request = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url).endsWith('/approve') && init?.method === 'POST')
  expect(JSON.parse(String(request![1]!.body))).toMatchObject({ decision: 'approve', comment: '' })
  await act(async () => root.unmount()); client.clear()
})

it('keeps recorded desktop and phone pictures in the hero with desktop first', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<Screenshots hero shots={{ commit: 'abc', shots: [
    { artifact: 'phone', view: 'phone', path: '/' }, { artifact: 'desktop', view: 'desktop', path: '/' },
  ] }} head="abc" />)
  expect([...el.querySelectorAll('figcaption')].map(e => e.textContent)).toEqual(['Desktop', 'Phone'])
  expect(el.querySelector('.ch-shots-hero .shot-row')).not.toBeNull()
  expect(el.querySelector('details')).toBeNull()
})

it('keeps acceptance independent of deployment reads', async () => {
  const v = view()
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => String(url).endsWith('/deployments')
    ? new Response(JSON.stringify({ error: 'Not permitted' }), { status: 403 })
    : new Response(JSON.stringify(String(url).endsWith('/question') ? { adds: null } : {})))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
  for (let j = 0; j < 3; j++) await act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
  expect(el.textContent).toContain('Deployments update separately.')
  expect(el.querySelector('.decision-form')).not.toBeNull()
  expect(el.textContent).not.toContain('This adds it to the app’s main version.')
  await act(async () => root.unmount()); client.clear()
})

it('uses the written summary ahead of the agent report and never labels the request as a report', () => {
  const v = view(); v.report = '- Added a parent form.\n- Saved the submitted permission slips.'
  let el = document.createElement('div'); el.innerHTML = render(v)
  expect([...el.querySelectorAll('.ch-overview-line li')].map(e => e.textContent)).toEqual(['Adds the winter ride calendar.'])
  v.change.summary = ''
  el = document.createElement('div'); el.innerHTML = render(v)
  expect([...el.querySelectorAll('.ch-overview-line li')].map(e => e.textContent)).toEqual(['Added a parent form.', 'Saved the submitted permission slips.'])
  v.report = 'Project work.'; v.change.summary = v.change.body
  el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.textContent).toContain('Reading the conversation…')
  expect(el.textContent).not.toContain('Project work')
})

it('uses two named cards and separates the fixed label, value and evidence columns', () => {
  const el = document.createElement('div'); el.innerHTML = render(view())
  expect(el.querySelector('.ch-change-card')?.getAttribute('aria-label')).toBe('The change')
  expect([...el.querySelectorAll('.ch-overview > section > h2')].map(e => e.textContent)).toEqual(['Your decision'])
  expect([...el.querySelectorAll('.ch-change-card .ch-row-label')].map(e => e.textContent)).toEqual(['Preview', 'You asked', 'What was made', 'Checks', 'Goes live', 'Reviewer', 'Release'])
  for (const row of el.querySelectorAll('.ch-change-card .ch-overview-line')) {
    expect(row.children).toHaveLength(3)
    expect(row.children[1].classList.contains('ch-value')).toBe(true)
    expect(row.querySelector('.ch-technical')?.closest('.ch-value')).toBe(row.querySelector('.ch-technical') ? row.children[1] : undefined)
  }
})

it('marks only the eligible viewer’s turn, excluding working, checking and closed changes', () => {
  const v = view()
  expect(viewerTurn(v)).toBe(true)
  expect(render(v)).toContain('ch-your-turn')
  v.approval = { ...v.approval!, viewer: 'no' }; v.waitingOn.you = 'no'
  expect(viewerTurn(v)).toBe(false)
  expect(render(v)).not.toContain('ch-your-turn')
  v.approval = { ...v.approval!, viewer: 'yes' }
  v.detail!.steps.push(step('scan', 'running')); v.waitingOn = { ...v.waitingOn, kind: 'work', says: 'working', you: 'no' }
  expect(viewerTurn(v)).toBe(false)
  v.detail!.steps = [approval]
  for (const state of ['shipped', 'abandoned']) { v.waitingOn = { ...v.waitingOn, kind: 'closed', says: state, you: 'no' }; v.change.state = state as ChangeView['change']['state']; expect(viewerTurn(v)).toBe(false) }
})

it('identifies offered held recovery and keeps its action labels in decision order', () => {
  const v = view(); v.change.state = 'in_progress'; v.state = 'blocked'; v.waitingOn = { ...v.waitingOn, kind: 'person', step: 'agent/pi', says: 'agent/pi (needs_human)', tone: 'warn', you: 'yes' }
  v.detail!.steps = [step('agent/pi', 'needs_human', { actions: ['Choose the start time.'] })]
  v.next = [{ act: 'send-back' }, { act: 'abandon' }] as ChangeView['next']
  v.detail!.next = [{ act: 'retry', stepId: 'agent/pi', step: 'agent/pi' }]
  expect(viewerTurn(v)).toBe(true)
  expect(heldLabels(v)).toEqual(['Send back…', 'Abandon change'])
  const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><StatePanel view={v} /></QueryClientProvider></MemoryRouter>)
  expect(html).toContain('ch-state tone-warn')
  expect(html).toContain('agent/pi (needs_human)')
})

it('keeps one decision screen measured to the content column and closes without deciding', async () => {
  const bounds = { height: 84, left: 234, width: 900, top: 120 }
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(() => bounds as DOMRect)
  const action = vi.fn(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  const labels = ['Accept', 'Send back…', 'Abandon change']
  const draw = (active: boolean) => act(() => root.render(<div className="ch-page"><section className="ch-tabs-sheet"><TurnDecision active={active} actions={labels} ready={<p>Ready: checks passed</p>}><p>Accepting puts this live.</p><DecisionChoices note="" setNote={() => {}} busy={false} accept={{ label: labels[0], run: action }} conversation={{ href: '?tab=conversation', run: action }} rework={{ label: labels[1], run: action }} abandon={{ label: labels[2], run: action }} /></TurnDecision></section></div>))
  draw(true)
  const page = el.querySelector<HTMLElement>('.ch-page')!, floater = el.querySelector<HTMLElement>('.ch-turn-floater')!, screen = el.querySelector<HTMLDialogElement>('dialog')!
  const sheet = page.querySelector<HTMLElement>('.ch-tabs-sheet')!
  expect(page.style.paddingBottom).toBe('84px')
  expect(sheet.style.maxHeight).toBe('')
  expect(sheet.style.overflowY).toBe('')
  expect(floater.style.left).toBe('234px'); expect(floater.style.width).toBe('900px')
  expect(screen.style.getPropertyValue('--page-left')).toBe('234px')
  expect(screen.style.getPropertyValue('--page-width')).toBe('900px')
  expect(el.querySelector<HTMLElement>('#decision')!.hidden).toBe(true)
  expect(el.querySelector('#decision button, #decision input')).toBeNull()
  expect(el.querySelectorAll('dialog')).toHaveLength(1)
  expect([...el.querySelectorAll('button')].map(b => b.textContent)).toEqual(['Decide…'])
  act(() => floater.querySelector('button')!.click())
  expect(action).not.toHaveBeenCalled()
  expect(screen.open).toBe(true)
  expect(floater.style.visibility).toBe('hidden')
  expect(screen.textContent).toContain('Ready: checks passed')
  expect(screen.textContent).toContain('Accepting puts this live.')
  expect([...screen.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(labels)
  expect([...screen.querySelectorAll('.ch-decision-choices button')].map(b => b.className)).toEqual(['primary', '', 'danger'])
  act(() => screen.querySelector<HTMLButtonElement>('header button')!.click())
  expect(screen.open).toBe(false)
  expect(screen.querySelector('button, input')).toBeNull()
  expect(floater.style.visibility).toBe('')
  expect(action).not.toHaveBeenCalled()
  Object.assign(bounds, { left: 0, width: 390, height: 96 })
  act(() => window.dispatchEvent(new Event('resize')))
  expect(page.style.paddingBottom).toBe('96px')
  // The document keeps normal page scrolling with room below its last content.
  bounds.top = 40
  act(() => window.dispatchEvent(new Event('scroll')))
  expect(page.style.paddingBottom).toBe('96px')
  expect(sheet.style.maxHeight).toBe('')
  expect(sheet.style.overflowY).toBe('')
  expect(floater.style.left).toBe('0px'); expect(floater.style.width).toBe('390px')
  expect(screen.style.getPropertyValue('--page-left')).toBe('0px')
  expect(screen.style.getPropertyValue('--page-width')).toBe('390px')
  draw(false)
  expect(el.querySelector('.ch-turn-floater, dialog')).toBeNull()
  expect(page.style.paddingBottom).toBe('')
  expect(sheet.style.maxHeight).toBe('')
  expect(sheet.style.overflowY).toBe('')
  expect(el.querySelector<HTMLElement>('#decision')!.hidden).toBe(false)
  expect(el.querySelector('#decision h2')?.textContent).toBe('Next steps')
  act(() => root.unmount()); el.remove(); vi.restoreAllMocks()
})

it('shows the latest chat reply and Continue chat as the only action before any run', async () => {
  const v = view(); v.checks = { tone: 'neutral', text: 'Not checked yet. Checks run when you press Check and land in the chat.', started: false }; v.change.summary = ''; v.change.state = 'in_progress'; v.state = ''; v.detail = null; v.approval = undefined; v.waitingOn = { ...v.waitingOn, kind: 'chat', says: 'chat open', tone: 'neutral', you: 'no' }
  v.next = [{ act: 'land' }] as ChangeView['next']
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).endsWith('/transcript') ? { sessions: [{ messages: [{ role: 'helper', text: 'Old answer.' }, { role: 'helper', text: 'The menu can be edited.\nOrders keep their names.' }] }], unreadable: 0 } : [])))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={[]} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
  await act(async () => { for (let i = 0; i < 12; i++) await new Promise(r => setTimeout(r, 0)) })
  expect(el.textContent).toContain('The agent saysThe menu can be edited.')
  expect(el.textContent).not.toContain('Old answer.')
  expect(el.textContent).not.toContain('No report from the agent')
  expect(el.querySelector('a[href$="tab=conversation"]')?.textContent).toBe('full conversation →')
  expect(el.textContent).toContain('Not checked yet. Checks run when you press Check and land in the chat.')
  for (const absent of ['0 checks', 'Read the app declarations', 'App contract', 'Run a workflow']) expect(el.textContent).not.toContain(absent)
  expect(el.querySelector('.ch-decision h2')?.textContent).toBe('Next steps')
  const buttons = [...el.querySelectorAll('.ch-decision-choices button')]
  expect(buttons.map(b => b.textContent)).toEqual(['Continue chat'])
  expect(buttons[0].className).toBe('primary')
  await act(async () => root.unmount()); client.clear()
})

it('groups unfinished checks and app contract, showing findings without evaluator instructions', () => {
  const steps = [step('script', 'fail', { command: 'sh checks/style.sh', reason: 'Missing a label. Add it before shipping.', actions: ['Resolve the reported issue.'] }, { name: 'style' }),
    ...['osv', 'gitleaks', 'security'].map(name => step('scan', 'running', {}, { id: name, name }))]
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={steps} contractNeedsAttention />)
  expect(el.querySelector('.ch-check-summary > p')?.textContent).toBe('Still running: Dependency scan, Secret scan, Security scan')
  expect([...el.querySelectorAll('.ch-check-exception')].map(e => e.firstChild?.textContent)).toEqual(['Style failed: Missing a label.', 'Changes to the app contract need checking.'])
  for (const text of ['Resolve the reported issue', 'Let the check finish', 'No passing result', 'Add it before shipping']) expect(el.textContent).not.toContain(text)
  const command = el.querySelector('.ch-technical')!
  expect(command.getAttribute('aria-hidden')).toBe('true')
  expect(command.textContent).toBe('sh checks/style.sh')
})

it('summarizes a script finding from its recorded output when it has no reason', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step('script', 'fail', { log: 'orders/views.py:24: print("order total", total)\nstyle: remove the print() calls above', command: 'sh checks/style.sh' }, { name: 'style' })]} />)
  expect(el.querySelector('.ch-check-exception')?.firstChild?.textContent).toBe('Style failed: orders/views.py:24: print("order total", total)')
})

it('keeps plain approval paragraphs without labels', () => {
  const brief = 'Adds a water surface that ripples where you tap.\n\nThe demo needs WebGL.'
  expect(approvalBrief(brief)).toEqual(['Adds a water surface that ripples where you tap.', 'The demo needs WebGL.'])
  expect(approvalBrief('A water demo.')).toEqual(['A water demo.'])
  expect(approvalBrief('  ')).toBeUndefined()
})

it('shows both approval paragraphs in the decision screen', async () => {
  const v = view()
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).endsWith('/brief') ? { brief: 'Adds water that ripples where you tap.\n\nRequires WebGL.' } : [])))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
  await act(async () => { for (let i = 0; i < 12; i++) await new Promise(r => setTimeout(r, 0)) })
  const paragraphs = [...el.querySelectorAll('#decision p')].map(p => p.textContent)
  expect(paragraphs).toContain('Adds water that ripples where you tap.')
  expect(paragraphs).toContain('Requires WebGL.')
  expect(el.querySelector('#decision')?.textContent).not.toContain('Your call:')
  await act(async () => root.unmount()); client.clear()
})

it('opens held recovery buttons only in Decide and shows the complete reason once there', async () => {
  const v = view()
  v.change.state = 'in_progress'
  v.waitingOn = { ...v.waitingOn, kind: 'check', step: 'preview', says: 'preview failed', tone: 'bad', you: 'yes' }; v.detail!.steps = [step('agent/pi'), step('preview', 'fail', { reason: 'The page never loaded. The screen stayed blank.' }), approval]
  v.detail!.next = [{ act: 'rework', stepId: 'agent/pi' }, { act: 'retry', stepId: 'preview' }, { act: 'override', stepId: 'preview' }, { act: 'override', stepId: 'approval' }] as NonNullable<ChangeView['detail']>['next']
  v.next = [{ act: 'send-back' }, { act: 'abandon' }] as ChangeView['next']
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).endsWith('/deployments') ? [] : { sessions: [] })))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
    expect(el.querySelector('#decision button')).toBeNull()
    expect(viewerTurn(v)).toBe(true)
    await act(async () => el.querySelector<HTMLButtonElement>('.ch-decide')!.click())
    const dialog = el.querySelector('dialog[open]')!
    expect(dialog.textContent?.match(/The page never loaded\./g)).toHaveLength(1)
    expect(dialog.textContent?.match(/The screen stayed blank\./g)).toHaveLength(1)
    expect([...dialog.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(['Accept anyway', 'Send back…', 'Abandon change'])
    expect(dialog.querySelector('details')).toBeNull()
    expect(dialog.textContent).not.toContain('Open the decision')
    expect(dialog.textContent).not.toContain('Accept despite failures')
  } finally {
    await act(async () => root.unmount())
    client.clear()
  }
})


it.each([undefined, 'Malformed app declaration', 'Starting app declaration unread'])('shows the accepted contract and submits its digest inside Decide (unread=%s)', async unread => {
  const v = view()
  const adds = { digest: 'shown-digest', rows: [
    { name: 'secrets', added: ['PAYMENT_KEY'], removed: [] },
    { name: 'services', added: [], removed: [], unknown: unread === 'Malformed app declaration' ? unread : undefined, baseUnread: unread === 'Starting app declaration unread' ? unread : undefined },
    { name: 'app parts', added: [], removed: [] },
  ], lines: [{ kind: 'added', text: 'Secrets: PAYMENT_KEY' }] }
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(
    String(url).endsWith('/question') ? { adds } : String(url).endsWith('/deployments') ? []
      : String(url).endsWith('/steps/build/live?from=0') ? { running: true, lines: ['Building the accepted image'], total: 1 } : {}
  )))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
    for (let j = 0; j < 3; j++) await act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
    await act(async () => el.querySelector<HTMLButtonElement>('.ch-decide')!.click())
    for (let j = 0; j < 3; j++) await act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
    expect(el.querySelector<HTMLButtonElement>('dialog .primary')?.disabled).toBe(false)
    const dialog = el.querySelector('dialog[open]')!
    expect(dialog.textContent).toContain('PAYMENT_KEY')
    if (unread) expect(dialog.textContent).toContain(unread)
    expect(dialog.querySelector('details')).toBeNull()
    vi.spyOn(dialog.querySelector<HTMLElement>('.decision-meaning')!, 'getBoundingClientRect').mockReturnValue({ top: 100, bottom: 140 } as DOMRect)
    await act(async () => dialog.querySelector<HTMLButtonElement>('.primary')!.click())
    const vote = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url).endsWith('/steps/approval/approve') && init?.method === 'POST')
    expect(vote).toBeDefined()
    expect(JSON.parse(vote![1]!.body as string)).toMatchObject({ decision: 'approve', adds: 'shown-digest', alsoApproves: ['Merge and release the accepted work.'] })


  } finally { await act(async () => root.unmount()); client.clear() }
})

it.each(['send-back', 'rework'])('records the approver’s rejection and note when %s offers Send back', async offeredAct => {
  const v = view()
  if (offeredAct === 'send-back') {
    v.next.push({ act: 'send-back' } as ChangeView['next'][number])
    v.detail!.next = v.detail!.next.filter(a => a.act !== 'rework')
  }
  const sent: { url: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') sent.push({ url: String(url), body: JSON.parse(String(init.body)) })
    return new Response(JSON.stringify(String(url).endsWith('/deployments') ? [] : {}))
  })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const settle = () => act(async () => { for (let i = 0; i < 12; i++) await new Promise(r => setTimeout(r, 0)) })
  const click = async (label: string) => act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === label)!.click())
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
    await settle()
    await click('Decide…')
    await settle()
    await click('Send back…')
    const field = el.querySelector<HTMLTextAreaElement>('dialog textarea')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(field, 'Keep the dinner invitation.')
      field.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await click('Send to agent')
    expect(sent).toEqual([{ url: `/api/v1/steps/${approval.id}/approve`, body: expect.objectContaining({ decision: 'reject', comment: 'Keep the dinner invitation.' }) }])
  } finally { await act(async () => root.unmount()); client.clear() }
})

it('preserves Continue when the server offers a stage advance', () => {
  const v = view(); v.change.state = 'in_progress'; v.state = 'ready to advance'; v.waitingOn = { ...v.waitingOn, kind: 'ready', says: 'ready to continue', tone: 'warn', step: null, you: 'yes' }
  v.detail!.steps = [step('script')]
  v.detail!.next = [{ act: 'advance' }] as NonNullable<ChangeView['detail']>['next']
  const el = document.createElement('div'); el.innerHTML = render(v)
  expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Continue')).toBe(true)
})

it.each([
  { advanced: true },
  { advanced: false, blocking: [{ name: 'Tests', status: 'fail' }], missing: ['Review'] },
  { advanced: false, followUpsPending: true },
])('continues the offered run and shows its result: %j', async result => {
  const v = view(); v.change.state = 'in_progress'; v.state = 'ready to advance'; v.waitingOn = { ...v.waitingOn, kind: 'ready', says: 'ready to continue', tone: 'warn', step: null, you: 'yes' }
  v.detail!.steps = [step('script')]
  v.detail!.next = [{ act: 'advance' }] as NonNullable<ChangeView['detail']>['next']
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(
    String(url).endsWith('/advance') ? result : [])))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Continue')!.click())
    expect(vi.mocked(fetch).mock.calls.some(([url, init]) => String(url).endsWith('/runs/run/advance') && init?.method === 'POST')).toBe(true)
    if (result.advanced) expect(el.textContent).toContain('Continued.')
    if (result.blocking) {
      expect(el.textContent).toContain('Blocked by: Tests (fail)')
      expect(el.textContent).toContain('Missing required checks: Review.')
    }
    if (result.followUpsPending) expect(el.textContent).toContain('Workflow finished. Follow-ups pending.')
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Stop this attempt')!.click())
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Yes')!.click())
    const stop = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url).endsWith('/runs/run/abandon') && init?.method === 'POST')
    expect(JSON.parse(stop![1]!.body as string)).toEqual({ reason: 'stopped from the change page' })
  } finally { await act(async () => root.unmount()); client.clear() }
})

it.each(['no', 'partly', 'unknown', 'yes'])('keeps the request assessment in the live decision context: %s', met => {
  const v = view()
  v.change.requestMet = met
  v.change.requestGap = 'The rest of the calendar was not read.'
  v.change.requestGapQuote = 'Read the whole calendar.'
  const html = render(v)
  if (met === 'no' || met === 'partly') {
    expect(html).toContain('falls short of the request: The rest of the calendar was not read.')
    expect(html).toContain('Based on: “Read the whole calendar.”')
  }
  else if (met === 'unknown') expect(html).toContain('could not check all of the request: The rest of the calendar was not read.')
  else expect(html).not.toContain('The rest of the calendar was not read.')
})

// The shield gives the whole check verdict, while the Checks row retains
// its evidence and turns red only for a failure.
it.each([
  ['pass', {}, 'ok', 'All checks passed'],
  ['fail', { reason: 'Build broke.' }, 'bad', 'Build failed'],
  ['pass', {}, 'unknown', 'Checks not all known'],
  ['running', {}, 'neutral', 'Checks running'],
] as const)('shows the checks shield for %s / %s', (status, evidence, tone, text) => {
  const v = view()
  v.checks = { tone, text, started: true }
  v.detail!.steps = [step('script', status, evidence, { name: 'Build', ...(tone === 'unknown' ? { statusLabel: 'unknown' } : {}) })]
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><StatePanel view={v} /><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>)
  const shield = el.querySelector('.ch-shield')!
  expect(shield.classList.contains(tone)).toBe(true)
  expect(shield.textContent).toBe(text)
  expect(shield.querySelector('a, button')).toBeNull()
  expect(shield.querySelector('svg')).not.toBeNull()
  const row = [...el.querySelectorAll('.ch-change-card .ch-overview-line')].find(e => e.querySelector('.ch-row-label')?.textContent === 'Checks')!
  expect(row.classList.contains('ch-line-bad')).toBe(tone === 'bad')
})


it('keeps a called approval on the approval path without parent recovery controls', () => {
  const v = view()
  v.waitingOn = { ...v.waitingOn, kind: 'approval', step: 'child-approval', run: 'child', you: 'yes' }
  v.detail!.steps = [step('workflow/call', 'running', { childRunId: 'child' })]
  v.detail!.next = []
  expect(nextStep(v).primary).toBe('open-decision')
  const html = render(v)
  expect(html).toContain('Reading the called workflow’s checks…')
  expect(html).not.toContain('ch-turn-floater')
  expect(html).not.toContain('Accept anyway')
})

it('labels an unsummarized agent report and excludes verification claims while keeping identifiers', () => {
  const v = view(); v.checks = { tone: 'neutral', text: 'Not checked yet. Checks run when you press Check and land in the chat.', started: false }; v.change.summary = ''; v.report = 'Set WP_HOME for HTTPS.\nVerified with curl.\nKept WP_SITEURL unchanged.'
  const el = document.createElement('div'); el.innerHTML = render(v)
  const row = [...el.querySelectorAll('.ch-change-card .ch-overview-line')].find(r => r.querySelector('.ch-row-label')?.textContent === 'The agent says')!
  expect(row.textContent).toContain('WP_HOME')
  expect(row.textContent).toContain('WP_SITEURL')
  expect(row.textContent).not.toContain('Verified with')
  expect(el.textContent).not.toContain('What was made')
})

// A missing price must remain visible even when no tokens were recorded.
it.each([[0, 2, 'not priced'], [3000, 1, 'partly priced'], [0, 0, '$0.00']] as const)('qualifies the overview cost (%s, %s)', (costMicros, unpriced, expected) => {
  const v = view()
  v.usage = { total: 0, costMicros, unpriced }
  expect(render(v)).toContain(` · ${expected}`)
})

it('uses WaitingOn for decision status and hides Next steps during landing deployment', () => {
  const v = view()
  v.runs = [{ id: 'run', role: 'landing' }] as ChangeView['runs']
  v.detail!.steps = [step('build', 'running'), step('artifact/require', 'pending')]
  v.detail!.next = []
  v.waitingOn = { ...v.waitingOn, kind: 'work', run: 'run', step: 'build', says: 'Landing: deploying — step 1 of 2', you: 'no' }
  let el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.querySelector('[aria-label="Now"]')).not.toBeNull()
  expect(progressTitle(v, v.detail!.steps)).toBe('Building')
  expect(progressTitle(v, [step('tests', 'running')])).toBe('Landing')
  expect(el.querySelector('#decision')?.hasAttribute('hidden')).toBe(true)
  expect(el.textContent).not.toContain('Checks are running; you can accept when they finish.')
  v.detail!.steps[0].status = 'error'
  v.waitingOn = { ...v.waitingOn, kind: 'check', says: 'Deploy could not run', you: 'no' }
  el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.querySelector('#decision')?.hasAttribute('hidden')).toBe(false)
  expect(el.querySelector('.ch-deploy-result')).toBeNull()
  expect(el.querySelector('.ch-change-card')?.textContent).toContain('No release requested')
})

it('names a finished landing deploy and leaves the summary request note out once the change is closed', () => {
  const v = view()
  v.runs = [{ id: 'run', role: 'landing' }] as ChangeView['runs']
  v.detail!.steps = [step('build', 'pass', { url: 'https://app.example.test' })]
  v.detail!.next = []
  v.change.requestMet = 'unknown'; v.change.requestGap = 'the request is cut off.'
  v.waitingOn = { ...v.waitingOn, kind: 'closed', run: null, step: null, says: 'landed', you: 'no' }
  const el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.querySelector('.ch-decision h2')?.textContent).toBe('Next steps')
  expect(el.textContent).not.toContain('could not check all of the request')
})

// Only the reading's explicit gates=false makes a result advisory.
it.each([
  [false, 'fail', 'ok', 'All checks passed · 1 note'],
  [false, 'unknown', 'ok', 'All checks passed · 1 note'],
  [true, 'unknown', 'unknown', 'Checks not all known'],
  [true, 'fail', 'bad', 'Ways of working failed'],
  [undefined, 'unknown', 'unknown', 'Checks not all known'],
  [undefined, 'fail', 'bad', 'Ways of working failed'],
  [null, 'fail', 'bad', 'Ways of working failed'],
] as const)('uses gates=%s for a %s check in the shield and Checks row', (gates, verdict, tone, text) => {
  const v = view()
  v.checks = { tone, text, started: true }
  v.detail!.steps = [step('guidance/conformance', 'pass', {}, { checkAttention: gates !== false, checkReading: { checks: [
    { check: 'arbitrary-rule', gates, verdict, detail: 'Use shorter sentences in the README.' },
    { check: 'another-rule', gates: true, verdict: 'pass' },
  ] } })]
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><StatePanel view={v} /><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>)
  expect(el.querySelector('.ch-shield')?.classList.contains(tone)).toBe(true)
  expect(el.querySelector('.ch-shield')?.textContent).toBe(text)
  const summary = el.querySelector('.ch-overview-line .ch-check-summary')!
  expect(summary.textContent).toContain('Use shorter sentences in the README.')
  if (gates === false) {
    expect(checkAttention(v.detail!.steps)).toEqual([])
    const passed = document.createElement('div'); passed.innerHTML = renderToStaticMarkup(<CheckSummary checks={v.checks} steps={v.detail!.steps} />)
    expect(passed.textContent).toContain('All checks passed')
    expect(passed.querySelector('.ch-mark.ok')).not.toBeNull()
    expect(passed.querySelector('p')?.textContent).toBe('Note: Use shorter sentences in the README.')
  }
})

it('counts advisory problems as notes and omits passing and non-applicable results', () => {
  const steps = [step('guidance/conformance', 'pass', {}, { checkAttention: false, checkReading: { checks: [
    { check: 'a', gates: false, verdict: 'fail', detail: 'Shorten the introduction.' },
    { check: 'b', gates: false, verdict: 'unknown', detail: 'The examples need a review.' },
    ...['pass', 'not-applicable', 'skipped'].map(verdict => ({ check: verdict, gates: false, verdict })),
  ] } })]
  const el = document.createElement('div'); el.innerHTML = renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={steps} />)
  expect([...el.querySelectorAll('p')].map(p => p.textContent)).toEqual(['Note: Shorten the introduction.', 'Note: The examples need a review.'])
})

it('keeps advisory details separate from a gating failure', () => {
  const steps = [step('guidance/conformance', 'pass', {}, { checkAttention: true, checkReading: { checks: [
    { check: 'tone', gates: false, verdict: 'fail', detail: 'Shorten the introduction.' },
    { check: 'privacy', gates: true, verdict: 'fail', detail: 'Remove the exposed password.' },
  ] } })]
  const el = document.createElement('div'); el.innerHTML = renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={steps} />)
  expect(el.querySelector('.ch-check-exception')?.textContent).toBe('Standards check failed: Remove the exposed password.')
  expect(el.querySelector('p:last-child')?.textContent).toBe('Note: Shorten the introduction.')
})


it.each(['empty', 'failed'])('shows the original ask when the approval brief is %s', async (result) => {
  const v = view()
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => {
    if (String(url).endsWith('/brief')) return result === 'failed'
      ? new Response(JSON.stringify({ error: 'Model unavailable' }), { status: 500 })
      : new Response(JSON.stringify({ brief: '' }))
    return new Response(JSON.stringify([]))
  })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
    await act(async () => { for (let i = 0; i < 12; i++) await new Promise(r => setTimeout(r, 0)) })
    expect(el.querySelector('#decision')?.textContent).toContain(`You asked${v.change.requestSummary || (v.change.body || v.change.title).split("\n")[0]}`)
  } finally {
    await act(async () => root.unmount()); client.clear()
  }
})

function landingResult(status: 'pass' | 'fail') {
  const v = view()
  v.change.state = 'shipped'
  v.waitingOn = { ...v.waitingOn, kind: 'closed', you: 'no', step: null, says: 'Landed' } as ChangeView['waitingOn']
  v.stateDetail = 'deploy (fail): The app container exited during startup.'
  v.detail!.run.createdAt = '2026-11-05T01:00:00Z'
  v.detail!.steps = [step('build', 'pass', { runtime: true, bundle: 'bundle' }, { finishedAt: '2026-11-05T01:01:12Z' })]
  v.detail!.next = []
  v.next = []
  v.runs = [{ ...v.detail!.run, role: 'landing' }] as ChangeView['runs']
  v.evidence!.landingReleases = [{ number: 7, deployment: 'main', state: status === 'pass' ? 'released' : 'failed', failure: 'starting the app' }] as NonNullable<ChangeView['evidence']>['releases']
  return v
}

// A landed change leads with its release; the preview of the landed commit
// follows it rather than opening the overview.
it('puts a shipped change\'s Release row before its Preview row', () => {
  const el = document.createElement('div'); el.innerHTML = render(landingResult('pass'))
  const labels = [...el.querySelectorAll('.ch-change-card .ch-overview-line .ch-row-label')].map(l => l.textContent)
  expect(labels.indexOf('Release')).toBeGreaterThanOrEqual(0)
  expect(labels.indexOf('Release')).toBeLessThan(labels.indexOf('Preview'))
  expect(labels.filter(l => l === 'Release')).toHaveLength(1)
})

it.each(['pass', 'fail'] as const)('keeps an unheld %s landing in the fixed Release row without a scene', status => {
 const el = document.createElement('div'); el.innerHTML = render(landingResult(status))
 expect(el.querySelector('.ch-deploy-result')).toBeNull()
 const rows = [...el.querySelectorAll('.ch-change-card .ch-overview-line')]
 const row = rows.find(row => row.querySelector('.ch-row-label')?.textContent === 'Release')!
 expect(row).toBeDefined()
 expect(row.textContent).toContain(status === 'pass' ? 'Release 7 · live' : 'Deploy failed: starting the app')
 expect(row.querySelector('a')?.getAttribute('href')).toBe('/projects/club/releases/7')
})

it('locks the page while a decision is open and restores styles on close and unmount', () => {
  document.documentElement.style.overflow = 'auto'
  const el = document.createElement('div'), root = createRoot(el)
  try {
    act(() => root.render(<div className="ch-page"><TurnDecision active actions={[]}><p>Decision</p></TurnDecision></div>))
    act(() => el.querySelector<HTMLButtonElement>('.ch-decide')!.click())
    expect(document.documentElement.style.overflow).toBe('hidden')
    expect(document.body.style.position).toBe('fixed')
    expect(document.body.style.top).toBe('0px')
    act(() => el.querySelector<HTMLDialogElement>('dialog')!.close())
    expect(document.documentElement.style.overflow).toBe('auto')
    expect(document.body.style.position).toBe('')
    act(() => el.querySelector<HTMLButtonElement>('.ch-decide')!.click())
  } finally { act(() => root.unmount()) }
  expect(document.documentElement.style.overflow).toBe('auto')
  expect(document.body.style.position).toBe('')
  document.documentElement.style.overflow = ''
})

it('links the running Release row to its deployment without displaying a number or scene', () => {
 const v = landingResult('pass')
 v.evidence!.landingReleases![0].state = 'releasing'
 v.detail!.steps[0].status = 'running'
 v.waitingOn = { ...v.waitingOn, kind: 'work', you: 'no' }
 const el = document.createElement('div'); el.innerHTML = render(v)
 const row = [...el.querySelectorAll('.ch-overview-line')].find(row => row.querySelector('.ch-row-label')?.textContent === 'Release')!
 expect(row.textContent).toBe('ReleaseDeploying… · watch →')
 expect(row.querySelector('a')?.getAttribute('href')).toBe('/projects/club/releases/7')
 expect(el.querySelector('.deploy-scene')).toBeNull()
})

it('labels every approval fact, omits the internal step and keeps its meaning and actions', async () => {
  const v = view()
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).endsWith('/brief') ? { brief: 'Adds the November newsletter.' }
    : String(url).endsWith('/deployments') ? [{ feed: 'main', url: 'https://club.example.test' }]
    : String(url).endsWith('/question') ? { adds: { rows: ['secrets', 'services', 'app parts'].map(name => ({ name, added: [], removed: [] })), digest: 'current' } } : [])))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
    await vi.waitFor(async () => { await act(async () => {}); expect(el.textContent).toContain('Adds the November newsletter.') })
    await act(async () => el.querySelector<HTMLButtonElement>('.ch-decide')!.click())
    const dialog = el.querySelector('dialog')!
    expect([...dialog.querySelectorAll('.ch-row-label')].map(row => row.textContent)).toEqual(['You asked', 'What it does', 'Checks', 'Goes live'])
    const card = el.querySelector('.ch-change-card')!
    for (const label of ['You asked', 'What it does', 'Checks', 'Goes live']) {
      const value = (parent: Element) => {
        const cell = [...parent.querySelectorAll('.ch-overview-line')].find(row => row.querySelector('.ch-row-label')?.textContent === label)?.querySelector('.ch-value')?.cloneNode(true) as Element | undefined
        cell?.querySelectorAll('.ch-technical').forEach(e => e.remove())
        return cell?.textContent
      }
      if (label === 'You asked') expect(value(dialog)).toContain(value(card))
      else expect(value(card), label).toBe(value(dialog))
    }
    expect(dialog.querySelector('.ch-accept-rows')?.textContent).not.toContain(v.change.body)
    expect(dialog.querySelector('a[href$="/records/request"]')?.textContent).toBe('full request →')
    expect(dialog.textContent).toContain('Landing puts the change on main. Deployments update separately.')
    expect(dialog.textContent).not.toContain('· accept')
    expect(dialog.textContent).toContain('By accepting, you confirm: “I accept this work in full.”')
    expect([...dialog.querySelectorAll('.ch-decision-choices button')].map(b => b.textContent)).toEqual(['Accept', 'Send back…', 'Abandon change'])
    expect(dialog.querySelector('a[href$="tab=conversation"]')).toBeNull()
  } finally { await act(async () => root.unmount()); client.clear() }
})


it('keeps project notes beside the short conversation request and first Markdown report paragraph', async () => {
  const v = view(); v.checks = { tone: 'neutral', text: 'Not checked yet. Checks run when you press Check and land in the chat.', started: false }; v.change.summary = ''; v.report = ''
  v.change.chatRequest = true
  v.change.requestSummary = 'Make distant cards easy to reach.'
  v.notesDiff = { added: ['- Keep cards reachable. (Sam · change 9)'], removed: ['- Use fixed cards. (Sam · change 4)'] }
  vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).endsWith('/transcript')
    ? { sessions: [{ messages: [{ role: 'helper', text: 'All checks passed. [Review the approval](https://example.test/review).\n\nMore implementation detail.' }] }], unreadable: 0 }
    : String(url).endsWith('/brief') ? { brief: 'Brings distant cards into view.' } : [])))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><ChangeOverview view={v} current steps={v.detail!.steps} onChange={() => {}} workTab={() => '?tab=details'} /></QueryClientProvider></MemoryRouter>))
    await vi.waitFor(async () => { await act(async () => {}); expect(el.querySelector('.ch-chat-excerpt a')?.getAttribute('href')).toBe('https://example.test/review') })
    const card = el.querySelector('.ch-change-card')!
    expect(card.querySelectorAll('ins')).toHaveLength(1)
    expect(card.querySelectorAll('del')).toHaveLength(1)
    expect(card.querySelector('ins')?.textContent).toBe(v.notesDiff!.added[0])
    expect(card.querySelector('del')?.textContent).toBe(v.notesDiff!.removed[0])
    expect(card.querySelector('.ch-request .ch-value')?.textContent).toBe('Make distant cards easy to reach.')
    expect(card.querySelector('.ch-request a')?.getAttribute('href')).toBe(`/changes/${id}?tab=conversation`)
    expect(card.textContent).toContain('What it doesBrings distant cards into view.')
    expect(card.textContent).toContain('The agent saysAll checks passed.')
    expect(card.textContent).not.toContain('[Review the approval]')
    expect(card.textContent).not.toContain('More implementation detail.')
    expect(card.querySelector('a.ch-trailer[href$="tab=conversation"]')?.textContent).toBe('request →')
    expect([...card.querySelectorAll('a')].some(a => a.textContent === 'full conversation →')).toBe(true)
  } finally { await act(async () => root.unmount()); client.clear() }
})


it('renders the first agent report paragraph without cutting Markdown links', () => {
  const v = view(); v.change.summary = ''
  const address = 'https://example.test/' + 'approval/'.repeat(30)
  v.report = `[Review the approval](${address}).\n\nSecond paragraph stays in the full report.`
  const el = document.createElement('div'); el.innerHTML = render(v)
  expect(el.querySelector('.ch-chat-excerpt a')?.getAttribute('href')).toBe(address)
  expect(el.textContent).not.toContain('Second paragraph stays')
})

it('keeps acceptance on the change while deployment runs separately', async () => {
 const v=view()
 v.change.chatRequest = true
 v.change.requestSummary = 'Keep cards reachable.'
 vi.stubGlobal('fetch',vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(String(url).endsWith('/deployments') ? [{name:'staff',feed:'landing',url:'https://staff.example'}] : {})))))
 const client=newQueryClient(),el=document.createElement('div'),root=createRoot(el)
 try {
  await act(async()=>root.render(<MemoryRouter initialEntries={['/changes/change']}><QueryClientProvider client={client}><Routes>
   <Route path="/changes/:id" element={<ChangeOverview view={v} current steps={v.detail!.steps} onChange={()=>{}} workTab={()=>'?tab=details'} />} />
   <Route path="/projects/club/deployments/staff" element={<p>Watching the staff deployment</p>} />
  </Routes></QueryClientProvider></MemoryRouter>))
  expect(el.querySelector('.ch-request .ch-value')?.textContent).toBe('Keep cards reachable.')
  expect(el.querySelector('.ch-request a')?.getAttribute('href')).toBe(`/changes/${id}?tab=conversation`)
  await act(async()=>el.querySelector<HTMLButtonElement>('.ch-decide')!.click())
  await vi.waitFor(()=>expect(el.querySelector<HTMLButtonElement>('dialog .primary')?.disabled).toBe(false))
  vi.spyOn(el.querySelector<HTMLElement>('dialog .decision-meaning')!,'getBoundingClientRect').mockReturnValue({top:100,bottom:140} as DOMRect)
  await act(async()=>el.querySelector<HTMLButtonElement>('dialog .primary')!.click())
  await vi.waitFor(()=>expect(vi.mocked(fetch).mock.calls.some(([url, init]) => String(url).endsWith('/approve') && init?.method === 'POST')).toBe(true))
  expect(el.querySelector('dialog[open]')).not.toBeNull()
 } finally {act(()=>root.unmount());client.clear()}
})


it.each(['waiting for approval', 'cancelled', 'superseded'] as const)('keeps a landed change release truthful while %s', state => {
 const v = landingResult('pass')
 v.evidence!.landingReleases![0].state = state
 v.evidence!.landingReleases![0].deployment = 'production'
 const el = document.createElement('div'); el.innerHTML = render(v)
 const row = [...el.querySelectorAll('.ch-overview-line')].find(row => row.querySelector('.ch-row-label')?.textContent === 'Release')!
 expect(row.textContent?.toLowerCase()).toContain(state)
 expect(row.textContent).not.toContain('live')
 if (state === 'waiting for approval') expect(row.querySelector('a')?.getAttribute('href')).toBe('/projects/club/deployments/production')
})

it('keeps legacy step selectors from reopening evidence inside the timeline', async () => {
  const v = view()
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith(`/changes/${id}`) ? v : url.endsWith('/auth/me') ? {user:'sam'} : []
  ))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setQueryData(getKey(`/changes/${id}`), v)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/changes/${id}?tab=details&step=script`]}><Routes><Route path="/changes/:id" element={<ChangePage />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('.ch-details')).not.toBeNull())
    expect(el.querySelector('.step-card, .ch-check-evidence-row')).toBeNull()
    expect(el.querySelector('a[href*="/records/entry-script-0?run=run"]')).not.toBeNull()
  } finally { act(() => root.unmount()); client.clear() }
})


// A run with no preview step has no preview record: the try-it door stays,
// but no "logs" link leads to a record that says none was made.
it('links preview logs only when a preview was recorded', () => {
  const html = render(view())
  expect(html).toContain('Open it and try it')
  expect(html).not.toContain('/records/preview')
})

it('retains the preview run in the logs link', () => {
  const v = view()
  v.detail!.steps.push(step('preview', 'pass', {screenshots:{shots:[],unavailable:'No capture was recorded.'}}, {id:'boot',runId:'preview-run'}))
  const el = document.createElement('div'); el.innerHTML = render(v)
  expect([...el.querySelectorAll('a')].find(a=>a.textContent==='logs →')?.getAttribute('href')).toBe(`/changes/${v.change.id}/records/preview?run=preview-run`)
})

// The execution status alone would pass in Evidence and fail in Overview.
it('shows the server verdict in Overview and Evidence for a passed step with a blocking failure', () => {
 const v = view()
 v.checks = { tone: 'bad', text: 'Ways of working failed', started: true }
 const s = step('guidance/conformance', 'pass', {}, { isCheck: true, checkAttention: true, statusLabel: 'failed', checkReading: { checks: [{ check: 'privacy', verdict: 'fail', gates: true, detail: 'Remove personal data.' }] } })
 v.detail!.steps = [s]
 const el = document.createElement('div')
 el.innerHTML = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={featureClient()}><StatePanel view={v} /><CheckSummary checks={v.checks} steps={[s]} /><Evidence view={v} runs={[{run: {...v.detail!.run, status: 'done'}, steps: [s]}]} workTab={() => '#'} /></QueryClientProvider></MemoryRouter>)
 expect(el.querySelector('.ch-shield')?.textContent).toBe(v.checks.text)
 expect(el.textContent?.split(v.checks.text).length).toBeGreaterThan(2)
 expect(el.textContent).not.toContain('All checks passed')
 expect(el.textContent).toContain('Remove personal data.')
})

// The code link names what changed in words; the commit is a technical fact
// that lives on Details, not the link's text.
it('links the code by its change size, not its short commit', () => {
  const v = view(); (v as { head?: string }).head = '257bbb1c0ffee0000000000000000000000000000'
  const el = document.createElement('div'); el.innerHTML = render(v)
  const code = [...el.querySelectorAll('a')].find(a => a.getAttribute('href')?.endsWith('?tab=changes'))!
  expect(code.textContent).toBe('5 files +35 −15 · code →')
})
