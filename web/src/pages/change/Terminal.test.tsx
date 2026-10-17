import { describe, expect, it } from 'vitest'
import { collapseProgress } from './Terminal'

// A progress line the step repeats with a new count reads as one line
// updating in place; distinct lines, and repeats of a line with no number,
// are all kept.
describe('the step terminal', () => {
  it('shows a repeated progress line once, at its latest reading', () => {
    expect(collapseProgress(['building image', 'deploying… 8s elapsed', 'deploying… 16s elapsed', 'deploying… 24s elapsed', 'deployed: https://x'])).toEqual(
      ['building image', 'deploying… 24s elapsed', 'deployed: https://x'])
  })
  it('keeps distinct lines and repeated lines without numbers', () => {
    expect(collapseProgress(['ok', 'ok', 'step 1 of 3', 'pulled layer a', 'step 2 of 3'])).toEqual(['ok', 'ok', 'step 1 of 3', 'pulled layer a', 'step 2 of 3'])
    expect(collapseProgress(['test 1 passed', 'test 2 passed'])).toEqual(['test 1 passed', 'test 2 passed'])
    expect(collapseProgress(['uploading 10%', 'uploading 55%', 'done in 3s'])).toEqual(['uploading 55%', 'done in 3s'])
  })
})
