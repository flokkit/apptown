import { describe, expect, it } from 'vitest'
import { PRODUCT } from './brand'
import { shownActor } from './systemActors'

// AppTown's own acts and model calls read as the product, under the names
// recorded before and after the rename; people and runs read as recorded.
describe('AppTown\'s own names in history', () => {
  it('shows the system actors as the product', () => {
    for (const name of ['forge', 'forge-system', 'system', 'apptown-system', 'apptown']) {
      expect(shownActor(name)).toBe(PRODUCT)
    }
    expect(shownActor('priya')).toBe('priya')
    expect(shownActor('run/7')).toBe('run/7')
  })
})
