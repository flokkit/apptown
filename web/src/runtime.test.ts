import { expect, it } from 'vitest'
import { statusProps, failedRuntimeRefresh } from './runtime'

it('formats the server answer without qualifying it with other evidence', () => {
 const props = statusProps({ word: 'unknown', reason: 'storage (s3) did not answer in time · old reading', checkedAt: '2026-11-07T01:23:00Z', retryAt: '2026-11-07T01:23:15Z', stale: true })
 expect(props.word).toBe('unknown')
 expect(props.help).toContain('storage (s3) did not answer in time · old reading · checked ')
 expect(props.help).toMatch(/next check \d{2}:\d{2}$/)
 expect(props.stacked).toBe(true)
 expect(props.help).toContain('old reading')
 expect(props.help).not.toMatch(/AM|PM/)
})

it('keeps a browser refresh failure distinct from the server deployment answer', () => {
 const answer = { observation: 'running' as const, targetName: 'Host', observationStale: false, status: { word: 'working' as const, reason: '', checkedAt: '2026-11-07T01:23:00Z', stale: false } }
 const shown = failedRuntimeRefresh(answer, 'network unavailable')
 expect(shown.status.word).toBe('unknown')
 expect(shown.status.reason).toContain('The status could not be refreshed: network unavailable')
 expect(shown.status.checkedAt).toBe(answer.status.checkedAt)
 expect(answer.status.word).toBe('working')
})

it('renders server reasons once without inventing a freshness phrase', () => {
 expect(statusProps({word:'unknown', reason:'not checked yet', stale:false}).help).toBe('not checked yet')
 expect(statusProps({word:'unknown', reason:'old reading', checkedAt:'2026-11-07T01:23:00Z', stale:true}).help.match(/old reading/g)).toHaveLength(1)
 const props = statusProps({word:'waiting', reason:'release 18 live — first check pending', appliedAt:'2026-11-07T06:32:39Z', stale:false})
 expect(props.word).toBe('waiting')
 expect(props.help).toContain('release 18 live — first check pending · applied ')
 expect(props.help).not.toMatch(/not checked yet|old reading|checked /)
})
