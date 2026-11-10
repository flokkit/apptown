import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { BackgroundReading } from './BackgroundReading'

// A check that was tried and failed has been checked: it says when, not
// that no check has happened.
it('names the last attempt of a check that has never succeeded', () => {
  const html = renderToStaticMarkup(<BackgroundReading reading={{ waiting: false, stale: false, unknown: true, attemptedAt: '2026-11-09T22:14:00Z', failure: 'ssh: connect to host preview.example.test port 22: Connection refused' }} />)
  expect(html).toContain('Last tried')
  expect(html).not.toContain('No completed background check yet')
})
