// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it } from 'vitest'
import type { ChangeView, Step } from '../../api'
import { newQueryClient } from '../../data'
import { DeployResult } from './DeployResult'

const view = { detail: { run: { createdAt: '2026-11-05T01:00:00Z' } }, evidence: { releases: [{ number: 7 }, { number: 6 }] } } as ChangeView
const step = { id: 'deploy', type: 'deploy', status: 'pass', finishedAt: '2026-11-05T01:01:12Z', evidence: { url: 'https://club.example.test' } } as Extract<Step, { type: 'deploy' }>
function draw(s = step, v = view) {
  return renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><DeployResult view={v} step={s} /></QueryClientProvider>)
}
it('ends a completed landing with the known release, address, duration and app link, without replay', () => {
  const html = draw()
  for (const text of ['Release 7 is live', 'https://club.example.test', 'Landing took 72s', 'Open the app']) expect(html).toContain(text)
  expect(html).not.toContain('celebrate')
  expect(draw(step, { ...view, evidence: { ...view.evidence, releases: [] } })).toContain('>Live</h3>')
})
it('names the recorded failed stage and reason, and says the app is down only once it was replaced', () => {
  const html = draw({ ...step, status: 'fail', evidence: { failedStage: 'starting', reason: 'The app container exited.' } })
  for (const text of ['Stopped while starting the app', 'The app container exited.']) expect(html).toContain(text)
  expect(html).not.toContain('not being served')
  expect(draw({ ...step, status: 'fail', evidence: { failedStage: 'checking', reason: 'No answer.' } })).toContain('The app is not being served until the next deploy.')
  expect(html).not.toContain('still serving')
  expect(html).not.toContain('Open the app')
})
