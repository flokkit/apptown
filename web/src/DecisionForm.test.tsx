// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from './data'
import { DecisionForm, landingMeansFor } from './DecisionForm'

// Record ids are UUIDv7 strings.
const STEP = '00000000-0007-7000-8000-000000000000'

// settle lets the form's reads answer.
const settle = () => act(async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0)) })

const detail = { stages: ['plan', 'review'], landingMeans: ['it merges and ships on my say-so'] }

describe('an acceptance shows what the landing would ask', () => {
  it('only for the approval in the last stage', () => {
    expect(landingMeansFor(detail, { type: 'approval', stage: 'review' })).toEqual(detail.landingMeans)
    expect(landingMeansFor(detail, { type: 'approval', stage: 'plan' })).toBeUndefined()
    expect(landingMeansFor(detail, { type: 'script', stage: 'review' })).toBeUndefined()
    expect(landingMeansFor({ ...detail, landingMeans: [] }, { type: 'approval', stage: 'review' })).toBeUndefined()
  })
  it('beside the step’s own sentence, before the person decides', () => {
    const html = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}>
      <DecisionForm stepId={STEP} means="I read it" alsoMeans={detail.landingMeans} onDone={() => {}} />
    </QueryClientProvider>)
    expect(html).toContain('Approving means: “I read it”')
    expect(html).toContain('If you may approve the landing, this also approves it: “it merges and ships on my say-so”')
  })

  // The words shown go back with the approval: the server records them only
  // if the landing still asks exactly that.
  it('sends back what it showed of the landing', async () => {
    const sent: Record<string, unknown>[] = []
    // The question is read first (no list: an approval of no commit).
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => { if (init?.body) sent.push(JSON.parse(String(init.body))); return new Response('{}') }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}><DecisionForm stepId={STEP} means="I read it" alsoMeans={detail.landingMeans} onDone={() => {}} /></QueryClientProvider>)
    })
    await settle()
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Approve')!.click() })
    expect(sent[0]).toMatchObject({ decision: 'approve', alsoApproves: detail.landingMeans })
    act(() => root.unmount())
    vi.unstubAllGlobals()
  })
})

// On a phone the comment and the two decisions are one bar held at the foot
// of the screen while the approval is in view (styles.css .decision-bar);
// what approving means stays above it, in the same panel.
describe('the decision bar', () => {
  const doc = (compact?: boolean) => {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}>
      <DecisionForm stepId={STEP} means="I read it" onDone={() => {}} approveLabel="Approve and land" rejectLabel="Reject and send back" compact={compact} />
    </QueryClientProvider>)
    return el
  }
  it('holds the comment and both decisions, after what approving means', () => {
    const el = doc()
    const bar = el.querySelector('.decision-bar')!
    expect([...bar.querySelectorAll('button')].map((b) => b.textContent)).toEqual(['Approve and land', 'Reject and send back'])
    expect(bar.querySelector('input[aria-label="comment"]')).not.toBeNull()
    expect(bar.textContent).not.toContain('Approving means')
    expect(el.textContent!.indexOf('Approving means')).toBeLessThan(el.textContent!.indexOf('Approve and land'))
  })
  it('is not drawn inline in a list row', () => {
    expect(doc(true).querySelector('.decision-bar')).toBeNull()
  })
})

// An approval of a commit shows what that commit adds, naming what was
// read, and the approval sends back the digest of the list it showed.
describe('what the change adds', () => {
  const question = {
    step: 'accept', status: 'pending', means: 'I read it', instructions: '', landingMeans: [],
    adds: { base: 'b', head: 'h', digest: 'd1', rows: [], readFrom: 'secrets, services and app parts', nothingNew: '',
      lines: [{ kind: 'added', text: 'Secrets: STRIPE_KEY' }, { kind: 'unknown', text: 'App parts: unknown, .forge/app.yaml at h does not parse' }] },
  }
  const mount = async (answer: unknown) => {
    const sent: { url: string; body: Record<string, unknown> }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url).endsWith('/question')) return new Response(JSON.stringify(answer))
      sent.push({ url: String(url), body: JSON.parse(String(init?.body)) })
      return new Response('{}')
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={newQueryClient()}><DecisionForm stepId={STEP} means="I read it" onDone={() => {}} /></QueryClientProvider>)
    })
    await settle()
    return { el, sent, done: () => { act(() => root.unmount()); vi.unstubAllGlobals() } }
  }
  const button = (el: HTMLElement, label: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === label)!

  it('is shown above the decision, naming what was read', async () => {
    const { el, done } = await mount(question)
    const section = el.querySelector('.adds')!
    expect(section.textContent).toContain('This change adds')
    expect(section.textContent).toContain('Approving accepts these into the app. Read from secrets, services and app parts.')
    expect([...section.querySelectorAll('li')].map((li) => li.className)).toEqual(['small adds-added', 'small adds-unknown'])
    done()
  })

  it('says what it read when nothing is new', async () => {
    const { el, done } = await mount({ ...question, adds: { ...question.adds, lines: [], nothingNew: 'Nothing new in secrets, services or app parts' } })
    expect(el.querySelector('.adds')!.textContent).toContain('Nothing new in secrets, services or app parts')
    done()
  })

  it('sends the digest with an approval, not with a rejection', async () => {
    const { el, sent, done } = await mount(question)
    await act(async () => { button(el, 'Approve').click() })
    await settle()
    await act(async () => { button(el, 'Reject').click() })
    await settle()
    expect(sent[0].body).toMatchObject({ decision: 'approve', adds: 'd1' })
    expect(sent[1].body.decision).toBe('reject')
    expect(sent[1].body).not.toHaveProperty('adds')
    done()
  })

  it('cannot approve before the question is read', () => {
    const html = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}>
      <DecisionForm stepId={STEP} means="I read it" onDone={() => {}} />
    </QueryClientProvider>)
    const el = document.createElement('div')
    el.innerHTML = html
    expect(button(el, 'Approve').disabled).toBe(true)
    expect(button(el, 'Reject').disabled).toBe(false)
  })
})
