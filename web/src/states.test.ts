import { describe, expect, it } from 'vitest'
import { changeStateLabel } from './states'

// One name per change state, in the glossary's words: a closed change whose
// work landed is shipped; one closed with nothing landed is finished.
describe('change state words', () => {
  it('says shipped only when the work landed, finished when nothing did', () => {
    expect(changeStateLabel('shipped', true)).toBe('shipped')
    expect(changeStateLabel('shipped', false)).toBe('finished')
  })
  // AppTown's own nothing-to-change close is a recorded fact (closedAs);
  // the same reason typed by a person is still an abandonment.
  it('says closed: nothing to change only when AppTown recorded that close', () => {
    expect(changeStateLabel('abandoned', undefined, 'nothing-to-change')).toBe('closed: nothing to change')
    expect(changeStateLabel('abandoned')).toBe('abandoned')
    expect(changeStateLabel('abandoned', undefined, '')).toBe('abandoned')
    expect(changeStateLabel('abandoned', undefined, 'another-kind')).toBe('abandoned')
  })
  it('calls landing landing', () => {
    expect(changeStateLabel('landing')).toBe('landing')
    expect(changeStateLabel('in_progress')).toBe('in progress')
  })
})
