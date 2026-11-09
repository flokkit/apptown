// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient, getKey } from '../../data'
import { fmtWhen } from '../../format'
import { RestoreFromBackup, restorable } from './RestoreFromBackup'
import type { DeploymentView, ProjectBackup } from '../../wire.gen'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => { vi.unstubAllGlobals() })

const OPERATION = '00000000-0030-7000-8000-000000000000'
const copy = (over: Partial<ProjectBackup>): ProjectBackup => ({
  id: '00000000-0012-7000-8000-000000000000', projectId: null, project: 'shop', deployment: 'main', deploymentLabel: 'main', statusLabel: 'finished',
  takenAt: '2026-10-08T01:00:00Z', trigger: 'manual', actor: 'bls', path: '/p', bytes: 10, status: 'ok', store: 'store',
  items: [{ kind: 'need', name: 's3', file: 's3.tar.gz', bytes: 5, sha256: 'b', how: 'tar of /data', copy: 'live-files', caveat: 'object metadata is not in the copy' }],
  ...over,
} as ProjectBackup)

// A deployment is restored from its own copies (in the store or on this
// host, finished or partial) and from final copies kept from the project's
// taken-down deployments; never from a managed database's copy, another
// project's, another live deployment's, or a failed or pruned one.
it("offers a deployment its own copies and the project's kept final copies, nothing else", () => {
  const own = copy({ id: 'a' }), partial = copy({ id: 'b', status: 'partial' }), final = copy({ id: 'c', deployment: 'old', trigger: 'take-down' })
  const managed = copy({ id: 'd', items: [{ kind: 'need', name: 'postgres', file: 'p', bytes: 1, sha256: 'x', how: 'pg_dump', copy: 'declared', instance: 'db-1' }] as ProjectBackup['items'] })
  const other = copy({ id: 'e', deployment: 'staging' }), elsewhere = copy({ id: 'f', project: 'other' }), failed = copy({ id: 'g', status: 'failed' }), pruned = copy({ id: 'h', status: 'pruned' })
  expect(restorable([own, partial, final, managed, other, elsewhere, failed, pruned], 'shop', 'main').map(b => b.id)).toEqual(['a', 'b', 'c'])
})

// A restore runs as an operation: confirming says the known loss first,
// asks with the deployment's name, and hands back the operation to follow.
it('restores the chosen copy and hands back its operation', async () => {
  const final = copy({ id: 'c', deployment: 'old', deploymentLabel: 'old', trigger: 'take-down' })
  const body = { keep: 7, where: '/b', backups: [copy({}), final] }
  const client = newQueryClient()
  client.setQueryData(getKey('/backups'), body)
  const asked: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST' && url.endsWith('/projects/shop/deployments/main/backups/c/restore')) {
      asked.push(String(init.body))
      return new Response(JSON.stringify({ operationId: OPERATION, state: 'queued' }), { status: 202 })
    }
    return new Response(JSON.stringify(body))
  }))
  const closed = vi.fn()
  const el = document.createElement('div'), root = createRoot(el)
  document.body.appendChild(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><RestoreFromBackup slug="shop" env={{ name: 'main', displayLabel: 'main' } as DeploymentView} onClose={closed} /></MemoryRouter></QueryClientProvider>) })
    const restore = () => [...document.querySelectorAll('button')].find(b => b.textContent === 'Restore')!
    expect(restore().disabled).toBe(true)
    expect(document.body.textContent).toContain('from old, taken down')
    const radios = [...document.querySelectorAll<HTMLInputElement>('input[type="radio"]')]
    expect(radios).toHaveLength(2)
    await act(async () => { radios[1].click() })
    expect(document.body.textContent).toContain(`Restore main from the copy taken ${fmtWhen(final.takenAt)}?`)
    expect(document.body.textContent).toContain('Not in this set · s3: object metadata is not in the copy.')
    await act(async () => { restore().click() })
    await vi.waitFor(() => expect(closed).toHaveBeenCalledWith(OPERATION))
    expect(asked).toEqual([JSON.stringify({ confirm: 'main' })])
  } finally { act(() => root.unmount()); el.remove(); client.clear() }
})
