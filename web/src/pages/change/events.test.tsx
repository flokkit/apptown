// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { eventSentence } from './events'

describe('eventSentence', () => {
  it('says AppTown merged main into the change', () => {
    expect(eventSentence({ kind: 'main.merged', payload: { by: 'forge', step: 'rework', mainBranch: 'main', main: 'abcdef0123456789', sha: '0123456789abcdef' } }))
      .toBe('AppTown merged main (abcdef01) into this change before step rework, as commit 01234567.')
  })
  it('names the files when main could not be merged', () => {
    expect(eventSentence({ kind: 'main.conflict', payload: { step: 'rework', mainBranch: 'main', main: 'abcdef0123456789', conflicts: ['README.md', 'app.go'] } }))
      .toBe("AppTown could not merge main (abcdef01) into this change before step rework: README.md, app.go conflict. Catch up in the change's chat.")
  })
  it('leaves other events as recorded', () => {
    expect(eventSentence({ kind: 'commit.created', payload: { sha: 'x' } })).toBeNull()
  })
})
