// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import * as Sentry from '@sentry/browser'
import { reportingOptions, scrubLinkTokens } from './errors'

// An invite or password reset link's token is a live credential. A report
// captured while one is in the address bar or in a breadcrumb must not
// carry it.
describe('browser error reports', () => {
  afterEach(async () => { await Sentry.close(); window.history.replaceState(null, '', '/') })

  it('carry no invite or password reset token', async () => {
    const sent: string[] = []
    window.history.replaceState(null, '', '/invite/forge_invite_CANARY0123abcd')
    Sentry.init({
      ...reportingOptions('https://public@errors.example.test/1'),
      transport: () => ({
        send: async (envelope) => { sent.push(JSON.stringify(envelope)); return {} },
        flush: async () => true,
      }),
    })
    Sentry.addBreadcrumb({ category: 'navigation', data: { from: '/', to: '/invite/forge_invite_CANARY0123abcd' } })
    Sentry.captureException(new Error('render fault on /invite/forge_invite_CANARY0123abcd'))
    await Sentry.flush(2000)
    expect(sent.length).toBeGreaterThan(0)
    const all = sent.join('\n')
    expect(all).toContain('/invite/forge_invite_[removed]')
    expect(all).not.toContain('CANARY0123abcd')
  })

  it('leaves a report without a token as it was', () => {
    const event = { request: { url: 'https://forge.test/people' } }
    expect(scrubLinkTokens(event)).toBe(event)
  })
})
