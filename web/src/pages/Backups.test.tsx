// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../data'
import { BackupsView } from './Backups'

// Record ids are UUIDv7 strings.
const SET = '00000000-0012-7000-8000-000000000000'
const SAFETY = '00000000-0013-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'
const DOCKER_SET = '00000000-0020-7000-8000-000000000000'
const CLOUD_SET = '00000000-0021-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const set = {
  id: SET, projectId: PROJECT, project: 'shop', deployment: 'main', takenAt: '2026-10-08T01:00:00Z', trigger: 'manual', actor: 'bls', path: '/p', bytes: 10, status: 'ok',
  items: [{ kind: 'need', name: 'redis', file: 'redis.rdb', bytes: 5, sha256: 'a', how: 'save', copy: 'declared' }, { kind: 'need', name: 's3', file: 's3.tar.gz', bytes: 5, sha256: 'b', how: 'tar of /data', copy: 'live-files', caveat: 'object metadata is not in the copy' }],
}

describe('restoring a backup set', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  // RB-02 (review of 8 October 2026): a restore one item of which failed
  // answers 502 with its receipt. The page showed only the error line, so
  // nobody could see which item failed, why, or that another item had
  // already been put back. The receipt is shown, and not as a success.
  it('a partly failed restore shows each item, why it failed and the safety set', async () => {
    const client = newQueryClient()
    client.setQueryData(getKey('/backups'), { keep: 7, where: '/b', backups: [set] })
    const receipt = {
      backup: SET, from: 'main', safetyBackup: SAFETY, stopped: true, started: true, status: 'partly restored',
      items: [
        { kind: 'need', name: 'redis', outcome: 'failed', detail: 'the copy holds 2 keys and redis holds 1' },
        { kind: 'need', name: 's3', outcome: 'copied', detail: 'files copied, not verified', caveat: 'object metadata is not in the copy' },
      ],
      error: '1 item(s) did not restore; the safety set 13 holds what was there before',
    }
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST' && url.endsWith(`/backups/${SET}/restore`)) return new Response(JSON.stringify(receipt), { status: 502 })
      return new Response(JSON.stringify({ keep: 7, where: '/b', backups: [set] }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => {
        root.render(<QueryClientProvider client={client}><MemoryRouter><BackupsView /></MemoryRouter></QueryClientProvider>)
      })
      const button = (label: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === label)!
      // RB2-02: the known loss is on the set and in the question.
      expect(el.textContent).toContain('object metadata is not in the copy')
      await act(async () => { button('Restore over main').click() })
      expect(el.textContent).toContain('Not in this set — s3: object metadata is not in the copy.')
      await act(async () => { button('Restore').click() })
      // The restore's answer is on the page once its receipt is.
      await vi.waitFor(() => expect(el.textContent).toContain('shop/main: partly restored from the set taken 10-08 01:00'))
      const text = el.textContent ?? ''
      expect(text).toContain('shop/main: partly restored from the set taken 10-08 01:00')
      expect(text).toContain('redis: failed — the copy holds 2 keys and redis holds 1')
      expect(text).toContain('s3: files copied, not verified — object metadata is not in the copy')
      expect(text).toContain('what was there before is in a new safety set, which leaves out the same')
      expect(text).not.toContain('✓ shop/main')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  }, 3000)

  // A Docker host's set in the backup store is restored in place like one
  // on the forge's disk; only a managed database's copy is started from.
  it('a Docker set in the store keeps its restore; a managed copy does not', async () => {
    const client = newQueryClient()
    const docker = { ...set, id: DOCKER_SET, store: 'store', path: 'shop/main/x' }
    // A set on the forge's disk is said to be on this host.
    const cloud = { ...set, id: CLOUD_SET, deployment: 'prod', store: 'store', path: 'shop/prod/x', items: [{ kind: 'need', name: 'postgres', file: 'postgres.dump', bytes: 5, sha256: 'c', how: 'pg_dump', copy: 'declared', format: 'pg-custom', instance: 'db-1', database: 'app' }] }
    const body = { keep: 7, where: '/b', backups: [docker, cloud] }
    client.setQueryData(getKey('/backups'), body)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body))))
    const el = document.createElement('div')
    const root = createRoot(el)
    try {
      await act(async () => {
        root.render(<QueryClientProvider client={client}><MemoryRouter><BackupsView /></MemoryRouter></QueryClientProvider>)
      })
      const latest = [...el.querySelectorAll('table')][0].querySelectorAll('tbody tr')
      expect(latest[0].textContent).toContain('Restore over main')
      expect(latest[0].textContent).toContain('backup store')
      expect(latest[1].textContent).toContain('backup store')
      expect(latest[1].textContent).not.toContain('Restore over')
      // Where each set is kept, by its recorded place and path: a folder a
      // stopped move left behind is matched against it (OHB-02).
      const all = [...el.querySelectorAll('table')][1].querySelectorAll('tbody tr')
      expect(all[0].textContent).toContain('backup store')
      expect(all[0].textContent).toContain('shop/main/x')
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  }, 3000)
})
