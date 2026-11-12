// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { Step } from '../../api'
import { InlineHelp } from '../../InlineHelp'
import { CheckSummary } from './Overview'
import { AddsSection, ContractAttention, contractUnread } from '../../DecisionForm'
import type { ChangeAdds } from '../../wire.gen'
const step = (status: string, extra = {}) => ({ id: status, isCheck: true, checkAttention: !['pass','not_applicable'].includes(status), type: 'script', name: 'Build app', status, statusLabel: status, evidence: {}, ...extra } as Step)
const dom = (html: string) => { const el = document.createElement('div'); el.innerHTML = html; return el }
it('summarizes successful checks in one plain line', () => {
  const steps = [step('pass', { evidence: { command: 'npm test', log: 'All done' } })]
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'ok', text: 'All checks passed', started: true }} steps={steps} />))
  expect(el.textContent).toBe('✓ All checks passed')
  expect(el.querySelector('a')).toBeNull()
  expect(el.querySelector('details')).toBeNull()
  expect(el.querySelectorAll('.ch-check-record')).toHaveLength(0)

})
it.each(['fail', 'running', 'pending', 'skipped', 'needs_human', 'error'])('shows problems and omits non-applicability (%s)', status => {
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step(status, { evidence: { reason: 'The build needs work.' } }), step('not_applicable', { evidence: { reason: 'No dependencies.' } }), step('pass')]} />))
  if (status === 'running') {
    expect(el.textContent).toBe('Still running: Build app')
  } else if (status === 'pending') {
    expect(el.textContent).toBe('Not started: Build app')
  } else {
    expect(el.querySelector('.ch-check-exception')?.textContent).toBe(`Build app ${status === 'skipped' ? 'not run' : 'failed'}: The build needs work.`)
  }
  expect(el.textContent).not.toContain('passing result')
  expect(el.querySelector('a')).toBeNull()
  expect(el.textContent).not.toContain('No dependencies.')
  expect(el.querySelector('details')).toBeNull()
})
const adds = (rows: ChangeAdds['rows']) => ({ rows: [...['services', 'secrets', 'app parts'].filter(n => !rows.some(r => r.name === n)).map(name => ({ name, added: [], removed: [] })), ...rows], lines: [], digest: 'd' } as unknown as ChangeAdds)
it('only shows actual app contract changes in plain words', () => {
  const html = renderToStaticMarkup(<AddsSection adds={adds([
    { name: 'services', added: ['postgres'], removed: [] },
    { name: 'secrets', added: ['STRIPE_KEY'], removed: ['S3_PUBLIC_URL'] },
    { name: 'app parts', added: ['part worker'], removed: [] },
    { name: 'ways of working', added: ['some pack'], removed: [] },
  ])} />)
  for (const text of ['Changes the app contract', '+ needs a database (postgres)', '+ needs the secret STRIPE_KEY, which someone must set before it can run', '− no longer needs S3_PUBLIC_URL', '+ a new part: worker']) expect(html).toContain(text)
  expect(html).not.toContain('some pack')
  expect(renderToStaticMarkup(<AddsSection adds={adds([{ name: 'services', added: [], removed: [] }])} />)).toBe('')
  expect(renderToStaticMarkup(<AddsSection adds={adds([{ name: 'services', added: [], removed: [], unknown: 'Unreadable' }])} />)).toBe('')
})

it('keeps unread contract changes with attention, never a declaration of none', () => {
  for (const value of [undefined, adds([{ name: 'services', added: [], removed: [], unknown: 'Cannot read app.yaml' }]), adds([{ name: 'services', added: ['postgres'], removed: [], baseUnread: 'Cannot read main' }])]) {
    expect(contractUnread(value)).toBe(true)
    const html = renderToStaticMarkup(<ContractAttention adds={value} />)
    expect(html).toContain('Not checked yet: changes to the app contract')
    expect(html).not.toContain('Changes the app contract')
  }
  expect(renderToStaticMarkup(<ContractAttention adds={adds([])} />)).toBe('')
})
it('never treats exceptions or unread results as green', () => {
  for (const extra of [{ checkAttention: true, statusLabel: 'unknown' }, { checkAttention: true, statusLabel: 'excepted' }, { checkAttention: true, overrideBy: 'sam', overrideReason: 'Temporary exception' }, { checkAttention: true, checkReading: { unreadable: 'No report' } }, { checkAttention: true, checkReading: { checks: [{ check: 'Privacy', verdict: 'excepted', detail: 'Temporary exception' }] } }]) {
    const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step('pass', extra)]} />))
    expect(el.querySelector('.ch-check-exception')).not.toBeNull()
    expect(el.textContent).not.toContain('All checks passed')
  }
})

it('counts not-applicable rules as fine without showing them', () => {
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'ok', text: 'All checks passed', started: true }} steps={[step('pass', { type: 'guidance/conformance', checkReading: { checks: [{ check: 'Privacy', verdict: 'not-applicable', detail: 'No personal data.' }] } })]} />))
  expect(el.textContent).not.toContain('Privacy')
  expect(el.textContent).toContain('✓ All checks passed')
  expect(el.querySelector('details')).toBeNull()
  expect(el.textContent).not.toContain('not applicable')
})

it('shows app-contract help openly on the change page', () => {
  const el = dom(renderToStaticMarkup(<InlineHelp.Provider value><AddsSection adds={adds([{ name: 'services', added: ['postgres'], removed: [] }])} /></InlineHelp.Provider>))
  expect(el.querySelector('button[aria-expanded]')).toBeNull()
  expect(el.textContent).toContain('The app contract is what this app asks')
  expect(el.textContent).toContain('needs a database (postgres)')
})

it('treats a skipped standing-deployment rule like Details and says the result once', () => {
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'ok', text: 'All checks passed', started: true }} steps={[step('pass', { type: 'guidance/conformance', checkReading: { checks: [{ check: 'memory-within', verdict: 'skipped', detail: 'judged on the standing deployment in reviews, not on a preview' }] } })]} />))
  expect(el.textContent).toBe('✓ All checks passed')
  expect(el.textContent).not.toContain('failed')
})
it('states an unknown test count in one clear phrase', () => {
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step('pass', { type: 'tests', checkAttention: true })]} />))
  expect(el.querySelector('.ch-check-exception')?.textContent).toBe('Tests: number unknown')
})

it.each(['fail', 'error', 'needs_human'])('labels a rejected review and leads with structured findings (%s)', status => {
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step(status, { type: 'agent/review', name: 'Review', evidence: {
    reason: 'reviewer rejected: The preview looked styled. The requested adoption is incomplete.',
    findings: ['Two projects lack the required rule.'],
  } })]} />))
  expect(el.querySelector('.ch-check-exception')?.textContent).toBe('✗ Review failed Two projects lack the required rule. The preview looked styled. The requested adoption is incomplete.')
})

it('keeps the review conclusion beyond its positive opening and removes the redundant prefix', () => {
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step('fail', { type: 'agent/review', name: 'Review', evidence: {
    reason: 'reviewer rejected: The preview looked styled. The requested adoption is incomplete.',
  } })]} />))
  expect(el.querySelector('.ch-check-exception')?.textContent).toBe('✗ Review failed The preview looked styled. The requested adoption is incomplete.')
})

it('bounds a long rejected review excerpt without selecting a sentence', () => {
  const el = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[step('fail', { type: 'agent/review', name: 'Review', evidence: {
    summary: 'The preview looked styled. ' + 'The adoption is incomplete. '.repeat(40),
  } })]} />))
  expect(el.textContent).toContain('✗ Review failed The preview looked styled. The adoption is incomplete.')
  expect(el.textContent?.endsWith('…')).toBe(true)
  expect(el.querySelector('.ch-check-exception')!.textContent!.length).toBeLessThanOrEqual(260)
})

it('keeps unfinished execution unknown after the owner says its run stopped', () => {
 const el = dom(renderToStaticMarkup(<CheckSummary checks={{tone:'unknown',text:'Checks not all known',started:true}} steps={[step('running',{statusLabel:'stopped',checkAttention:true})]}/>))
 expect(el.textContent).toContain('Build app stopped')
 expect(el.textContent).not.toContain('Still running')
 expect(el.textContent).not.toContain('All checks passed')
})

// A step that has not started is "not started", never "still running"; after
// a failure the steps behind it never start, so they are not listed at all.
it('separates running from not started, and drops not started after a failure', () => {
  const layout = step('running', { id: 'layout', name: 'Check layout' })
  const standards = step('pending', { id: 'standards', name: 'Standards check' })
  const progress = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'unknown', text: 'Checks not all known', started: true }} steps={[layout, standards]} />))
  expect(progress.textContent).toContain('Still running: Check layout')
  expect(progress.textContent).not.toContain('Still running: Check layout, Standards check')
  expect(progress.textContent).toContain('Not started: Standards check')
  expect(progress.textContent).not.toContain('Checks not all known')
  const style = step('fail', { id: 'style', name: 'Style', evidence: { reason: 'Lines too long.' } })
  const scan = step('pending', { id: 'scan', name: 'Dependency scan' })
  const failed = dom(renderToStaticMarkup(<CheckSummary checks={{ tone: 'bad', text: 'A check failed', started: true }} steps={[style, scan]} />))
  expect(failed.textContent).not.toContain('Still running')
  expect(failed.textContent).not.toContain('Dependency scan')
  expect(failed.textContent).toContain('Style failed')
})

it('shows the server skip count without a pass mark', () => {
 const el = dom(renderToStaticMarkup(<CheckSummary checks={{tone:'ok', text:'1 passed, 1 skipped: Dependency scan', started:true}} steps={[step('pass'), step('not_applicable')]} />))
 expect(el.textContent).toBe('1 passed, 1 skipped: Dependency scan')
 expect(el.textContent).not.toContain('✓')
})
