import { expect, it } from 'vitest'
import { kickoffText } from './Console'

// The chat shows what the agent was asked, in the person's own words, not a
// pointer to a briefing nobody can see.
it('opens a change chat with the request itself', () => {
  const first = kickoffText('1', 'Feet catalog with pictures of different feet')
  expect(first).toContain('Feet catalog with pictures of different feet')
  expect(first).not.toContain('briefing')
  expect(kickoffText('go', 'Show the order total')).toContain('Show the order total')
  expect(kickoffText('1', '  ')).toContain('ask me first')
})
