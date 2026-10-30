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
const PROJECT = '00000000-0001-7000-8000-000000000000'
const DOCKER_SET = '00000000-0020-7000-8000-000000000000'
const CLOUD_SET = '00000000-0021-7000-8000-000000000000'
const OPERATION = '00000000-0030-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const set = {
  id: SET, projectId: PROJECT, project: 'shop', deployment: 'main', takenAt: '2026-10-08T01:00:00Z', trigger: 'manual', actor: 'bls', path: '/p', bytes: 10, status: 'ok',
  items: [{ kind: 'need', name: 'redis', file: 'redis.rdb', bytes: 5, sha256: 'a', how: 'save', copy: 'declared' }, { kind: 'need', name: 's3', file: 's3.tar.gz', bytes: 5, sha256: 'b', how: 'tar of /data', copy: 'live-files', caveat: 'object metadata is not in the copy' }],
}

describe('restoring a backup set', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  // A restore runs as an operation (soak finding 56): the page says it
  // started and links to the operation, whose page shows each step and
  // each item's outcome (what failed and why, and the safety set) as it
  // finishes. The known loss is said before the restore is asked for.
  it('a restore answers with its operation, which the page links to', async () => {
    const client = newQueryClient()
    client.setQueryData(getKey('/backups'), { keep: 7, where: '/b', backups: [set] })
    const asked: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST' && url.endsWith(`/backups/${SET}/restore`)) {
        asked.push(String(init.body))
        return new Response(JSON.stringify({ operationId: OPERATION, state: 'queued', url: `/api/v1/operations/${OPERATION}` }), { status: 202 })
      }
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
      expect(el.textContent).toContain('Not in this set · s3: object metadata is not in the copy.')
      await act(async () => { button('Restore').click() })
      await vi.waitFor(() => expect(el.textContent).toContain('shop/main: restore from the set taken 10-08 01:00 started.'))
      expect(asked).toEqual([JSON.stringify({ confirm: 'main' })])
      const follow = [...el.querySelectorAll('a')].find((a) => a.textContent === 'Follow it')
      expect(follow?.getAttribute('href')).toBe(`/operations/${OPERATION}`)
    } finally {
      act(() => root.unmount())
      client.clear()
    }
  }, 3000)

  // A Docker host's set in the backup store is restored in place like one
  // on AppTown's disk; only a managed database's copy is started from.
  it('a Docker set in the store keeps its restore; a managed copy does not', async () => {
    const client = newQueryClient()
    const docker = { ...set, id: DOCKER_SET, store: 'store', path: 'shop/main/x' }
    // A set on AppTown's disk is said to be on this host.
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
