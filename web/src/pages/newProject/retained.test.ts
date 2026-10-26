import { describe, expect, it } from 'vitest'
import { draftReducer, newDraft, originReady } from './draft'
import { captureCreation, matchingBundle, readCreation, retainCreation } from './retained'

const actor = '00000000-0001-7000-8000-000000000000'
const other = '00000000-0002-7000-8000-000000000000'
const destination = { remote: false, namespaceId: '' }
function memory(): Storage {
  const values = new Map<string, string>()
  return {
    get length() { return values.size }, clear: () => values.clear(),
    key: (n) => [...values.keys()][n] ?? null,
    getItem: (k) => values.get(k) ?? null, setItem: (k, v) => { values.set(k, v) }, removeItem: (k) => { values.delete(k) },
  }
}

describe('retained project creation intent', () => {
  it('isolates actors even when their display names could be the same', async () => {
    const store = memory()
    const saved = await captureCreation(actor, newDraft(), destination)
    retainCreation(store, saved)
    expect(readCreation(store, actor)).toEqual(saved)
    expect(readCreation(store, other)).toBeNull()
    store.setItem(`apptown.project-creation.v1.${other}`, JSON.stringify(saved))
    expect(() => readCreation(store, other)).toThrow('does not match this account')
    expect(() => readCreation(store, 'owner')).toThrow('current account')
  })
  it('freezes nested choices and both operation IDs while permitting a progress checkpoint', async () => {
    const store = memory()
    const draft = { ...newDraft(), description: 'Lunch orders', services: [{ capability: 'postgres', params: { version: '16' } }] }
    const saved = await captureCreation(actor, draft, destination)
    draft.services[0].params.version = '17'
    retainCreation(store, saved)
    const next = { ...saved, project: { id: other, slug: 'server-selected' }, pending: 'pack' as const }
    retainCreation(store, next)
    expect(readCreation(store, actor)?.draft.services[0].params?.version).toBe('16')
    expect(readCreation(store, actor)?.draft.creationRequestId).toBe(draft.creationRequestId)
    expect(readCreation(store, actor)?.draft.attachmentRequestId).toBe(draft.attachmentRequestId)
    expect(() => retainCreation(store, { ...next, draft: { ...next.draft, description: 'Different intent' } })).toThrow('different request')
    expect(() => retainCreation(store, { ...next, project: { id: actor, slug: 'other-project' } })).toThrow('identity cannot be replaced')
    expect(readCreation(store, actor)?.project?.slug).toBe('server-selected')
  })
  it('keeps malformed evidence and propagates unavailable storage', async () => {
    const store = memory()
    const key = `apptown.project-creation.v1.${actor}`
    store.setItem(key, '{invalid')
    expect(() => readCreation(store, actor)).toThrow('kept for recovery')
    expect(store.getItem(key)).toBe('{invalid')
    const saved = await captureCreation(actor, newDraft(), destination)
    store.removeItem(key)
    store.setItem = () => { throw new Error('storage full') }
    expect(() => retainCreation(store, saved)).toThrow('storage full')
    store.getItem = () => { throw new Error('storage unavailable') }
    expect(() => readCreation(store, actor)).toThrow('storage unavailable')
  })
  it.each(['empty', 'bundle'] as const)('allows abandoning an unfinished URL for %s', async (origin) => {
    let draft = draftReducer(newDraft(), { type: 'repo', set: { origin: 'url', sourceURL: 'https://' } })
    draft = draftReducer(draft, { type: 'repo', set: { origin, bundleFile: new File(['history'], 'source.bundle') } })
    expect(originReady(draft)).toBe(true)
    const saved = await captureCreation(actor, draft, destination)
    expect(saved.draft.origin).toBe(origin)
    expect(saved.draft.sourceURL).toBe('')
  })
  it('drops inactive source fields instead of retaining credentials from an abandoned source', async () => {
    const saved = await captureCreation(actor, { ...newDraft(), origin: 'empty', sourceURL: 'https://user:password@example.test/repo.git', sourceBranch: 'old-branch', gitSecret: 'OLD_CREDENTIAL' }, destination)
    expect(saved.draft.sourceURL).toBe('')
    expect(saved.draft.sourceBranch).toBe('')
    expect(saved.draft.gitSecret).toBe('')
  })
  it('does not retain credentials embedded in a source URL', async () => {
    await expect(captureCreation(actor, { ...newDraft(), origin: 'url', sourceURL: 'https://user:password@example.test/repo.git' }, destination)).rejects.toThrow('Remove credentials')
  })
  it('requires the original bundle bytes, not merely the same filename', async () => {
    const original = new File(['original history'], 'project.bundle')
    const saved = await captureCreation(actor, { ...newDraft(), origin: 'bundle', bundleFile: original }, destination)
    expect(saved.draft).not.toHaveProperty('bundleFile')
    await expect(matchingBundle(saved, new File(['original history'], 'renamed.bundle'))).resolves.toBeUndefined()
    await expect(matchingBundle(saved, new File(['other history'], 'project.bundle'))).rejects.toThrow('does not match')
    expect(saved.bundleDigest).toMatch(/^[0-9a-f]{64}$/)
  })
})
