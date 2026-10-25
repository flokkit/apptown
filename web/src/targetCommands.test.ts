// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { changedTargetSettings, editTarget, offerTarget, targetOffer } from './targetCommands'

afterEach(() => vi.unstubAllGlobals())

it('sends explicit clears against the captured settings revision without unchanged fields', async () => {
  const fetch = vi.fn(async () => new Response('{"ok":true,"target":{"settingsRevision":8}}'))
  vi.stubGlobal('fetch', fetch)
  const base = { domain: 'old.example', persistentVolumes: true, previews: true, needsMode: 'emulators' }
  const draft = { ...base, domain: '', persistentVolumes: false }
  const saved = await editTarget('host', 7, changedTargetSettings(base, draft))
  expect(saved.target?.settingsRevision).toBe(8)
  expect(fetch).toHaveBeenCalledOnce()
  const [path, request] = fetch.mock.calls[0] as unknown as [string, RequestInit]
  expect(path).toBe('/api/v1/targets/host')
  expect(request.method).toBe('PATCH')
  expect(JSON.parse(String(request.body))).toEqual({ expectedRevision: 7, domain: '', persistentVolumes: false })
})

it('sends populated offer caps through the target offer endpoint', async () => {
  const fetch = vi.fn(async () => new Response('{"ok":true}'))
  vi.stubGlobal('fetch', fetch)
  await offerTarget('host', targetOffer('team', ' 3 ', ['partners'], ['synthetic']))
  const [path, request] = fetch.mock.calls[0] as unknown as [string, RequestInit]
  expect(path).toBe('/api/v1/targets/host/offers')
  expect(request.method).toBe('POST')
  expect(JSON.parse(String(request.body))).toEqual({ group: 'team', maxStanding: 3, audiences: ['partners'], data: ['synthetic'] })
})

it('omits blank caps rather than inventing limits or empty restrictions', async () => {
  const fetch = vi.fn(async () => new Response('{"ok":true}'))
  vi.stubGlobal('fetch', fetch)
  await offerTarget('host', targetOffer('*', '  ', [], []))
  const [, request] = fetch.mock.calls[0] as unknown as [string, RequestInit]
  expect(JSON.parse(String(request.body))).toEqual({ group: '*' })
})

it.each(['abc', 'Infinity', '-1', '1.5', '9007199254740992'])('refuses invalid standing limit %s before serialization', (limit) => {
  expect(() => targetOffer('*', limit, [], [])).toThrow(/whole number/)
})

it.each(['', ' ', '0', ' 3 '])('keeps explicit unlimited and ordinary count semantics for %s', (limit) => {
  const caps = targetOffer('*', limit, [], [])
  expect(caps.maxStanding).toBe(limit.trim() ? Number(limit) : undefined)
})
