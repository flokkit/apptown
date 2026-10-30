import { describe, expect, it } from 'vitest'
import { RUNTIME_LIGHT, runtimeChip, runtimeDescription, runtimeLabel, type RuntimeObservation } from './runtime'
import { fmtStamp } from './format'
import type { RuntimeState } from './wire.gen'

describe('server runtime status', () => {
  it('maps every server state to a light and renders its word unchanged', () => {
    const states: Record<RuntimeState, string> = { running: 'pass', stopped: '', unknown: 'unknown', unreachable: 'fail' }
    for (const [state, light] of Object.entries(states)) expect(RUNTIME_LIGHT[state as RuntimeState]).toBe(light)
    for (const observation of Object.keys(states) as RuntimeState[]) {
      expect(runtimeLabel({ observation })).toBe(observation)
      expect(runtimeChip({ observation })).toBe(states[observation])
    }
  })
  it('keeps the host and first failure time in help', () => {
    const o: RuntimeObservation = { observation: 'unreachable', targetName: 'Club host', unreachableSince: '2026-10-31T00:00:00Z', observationStale: true }
    expect(runtimeDescription(o)).toBe(`AppTown can't reach Club host since ${fmtStamp('2026-10-31T00:00:00Z')}. The app may still be running.`)
  })
})

it('names the deployment host when its display name is absent', () => {
 expect(runtimeDescription({observation: 'unreachable', observationStale: true, targetName: '', unreachableSince: '2026-10-31T00:00:00Z'})).toBe(`AppTown can't reach the deployment host since ${fmtStamp('2026-10-31T00:00:00Z')}. The app may still be running.`)
})

it('omits the since clause when no failure time is available', () => {
 expect(runtimeDescription({ observation: 'unreachable', targetName: 'Club host', observationStale: true })).toBe("AppTown can't reach Club host. The app may still be running.")
})
