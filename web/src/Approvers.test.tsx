// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { expect, it } from 'vitest'
import { ApproversCard, WaitingStatus } from './Approvers'
import { getKey, newQueryClient } from './data'
import type { StepApprovers, WaitingOn } from './wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

async function card(answer: StepApprovers) {
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  client.setQueryData(getKey('/steps/s1/approvers'), answer)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ApproversCard stepId="s1" /></MemoryRouter></QueryClientProvider>))
  const text = el.textContent ?? '', links = [...el.querySelectorAll('a')].map(a => a.getAttribute('href'))
  act(() => root.unmount()); client.clear()
  return { text, links }
}

const reviewers = (people: string[]) => ({ kind: 'group', name: 'reviewers', label: 'reviewers', people })

it('names everyone who can approve under their role and group while there are fewer than five', async () => {
  const { text, links } = await card({ approvers: [{ kind: 'role', name: 'project-approver', label: 'Project approver', people: ['priya'], groups: [reviewers(['lee', 'mei'])] }], opener: 'tom' })
  for (const fact of ['Project approver', 'priya', 'reviewers', 'lee', 'mei', 'Not tom']) expect(text).toContain(fact)
  expect(links).toContain('/people/groups/reviewers')
})

it('counts a large group and links to it instead of listing its members', async () => {
  const { text, links } = await card({ approvers: [{ kind: 'role', name: 'project-approver', label: 'Project approver', people: ['priya'], groups: [reviewers(['kai', 'rua', 'ivy', 'zed'])] }] })
  expect(text).toContain('priya')
  expect(text).toContain('reviewers group · 4 members')
  for (const member of ['kai', 'rua', 'ivy', 'zed']) expect(text).not.toContain(member)
  expect(links).toContain('/people/groups/reviewers')
})

it('says when nobody holds the role', async () => {
  const { text } = await card({ approvers: [{ kind: 'role', name: 'project-approver', label: 'Project approver', people: [] }] })
  expect(text).toContain('Nobody holds this role yet')
})

// A failed change's status opens what went wrong, as the change page's advice
// says it, with the way to the change; a status with nothing to open stays
// a plain chip.
it('opens what went wrong from a failed status, and nothing from a plain one', async () => {
  const failed: WaitingOn = { kind: 'check', run: 'r1', step: 's2', checks: 1, who: [], you: '', says: 'Review failed', tone: 'bad' }
  const client = newQueryClient(), el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  client.setQueryData(getKey('/steps/s2/advice'), { text: 'The reviewer found the login form posts the password in the address.', recommend: 'send-back' })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter>
      <WaitingStatus waiting={failed} changeId="c1" /><span id="plain"><WaitingStatus waiting={{ ...failed, tone: 'busy', says: 'checks running' }} /></span>
    </MemoryRouter></QueryClientProvider>))
    expect(el.querySelector('#plain button')).toBeNull()
    await act(async () => el.querySelector<HTMLButtonElement>('button.chip-button')!.click())
    expect(document.body.textContent).toContain('posts the password in the address')
    expect(document.body.querySelector('a[href="/changes/c1"]')?.textContent).toBe('Open the change →')
  } finally { act(() => root.unmount()); client.clear(); el.remove() }
})

// Where there is no written advice (no model), the failed step's own
// recorded reason says what went wrong.
it('falls back to the failed step’s recorded reason when there is no advice', async () => {
  const failed: WaitingOn = { kind: 'check', run: 'r1', step: 's3', checks: 1, who: [], you: '', says: 'Style failed', tone: 'bad' }
  const client = newQueryClient(), el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  client.setQueryData(getKey('/steps/s3/advice'), { text: '', recommend: '' })
  client.setQueryData(getKey('/changes/c3'), { change: { id: 'c3' }, detail: { steps: [{ id: 's3', evidence: { reason: 'prettier: 3 files need formatting\nweb/src/a.tsx' } }, { id: 's4', evidence: { reason: 'reviewer rejected: The preview looked styled on desktop and phone. Everything else passed.', findings: ['config/application.php sets the scheme too early. When a visitor opens the site over HTTPS the links break.', 'Second problem.'] } }] } })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><WaitingStatus waiting={failed} changeId="c3" /></MemoryRouter></QueryClientProvider>))
    await act(async () => el.querySelector<HTMLButtonElement>('button.chip-button')!.click())
    expect(document.body.textContent).toContain('prettier: 3 files need formatting')
    expect(document.body.textContent).not.toContain('web/src/a.tsx')
  } finally { act(() => root.unmount()); client.clear(); el.remove() }
})

// A review leads with the problem it found, not its summary of what passed.
it('leads a failed review with its first problem, in one sentence', async () => {
  const failed: WaitingOn = { kind: 'check', run: 'r1', step: 's4', checks: 1, who: [], you: '', says: 'Review failed', tone: 'bad' }
  const client = newQueryClient(), el = document.body.appendChild(document.createElement('div')), root = createRoot(el)
  client.setQueryData(getKey('/steps/s4/advice'), { text: '', recommend: '' })
  client.setQueryData(getKey('/changes/c3'), { change: { id: 'c3' }, detail: { steps: [{ id: 's4', evidence: { reason: 'reviewer rejected: The preview looked styled on desktop and phone. Everything else passed.', findings: ['config/application.php sets the scheme too early. When a visitor opens the site over HTTPS the links break.', 'Second problem.'] } }] } })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><WaitingStatus waiting={failed} changeId="c3" /></MemoryRouter></QueryClientProvider>))
    await act(async () => el.querySelector<HTMLButtonElement>('button.chip-button')!.click())
    const panel = document.body.querySelector('.status-why')!.textContent
    expect(panel).toContain('The reviewer found 2 problems. The first:')
    expect(panel).toContain('config/application.php sets the scheme too early.')
    expect(panel).not.toContain('When a visitor opens')
    expect(panel).not.toContain('looked styled')
  } finally { act(() => root.unmount()); client.clear(); el.remove() }
})
