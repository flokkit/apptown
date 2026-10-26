import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../../data'
import type { Figure, TargetCapacity } from '../../wire.gen'
import { Capacity } from './Capacity'

const fig = (value: number): Figure => ({ known: true, value })
const none = (reason: string): Figure => ({ known: false, value: 0, reason })
const count = (apptown: number, other: number) => ({ apptown, other })

// A target's capacity says what a fixed host has, what its containers are
// promised and use, and its disk, with a reading it could not make as
// unknown, never 0; an elastic target says it grows, and estimates cost.
const render = (c: Partial<TargetCapacity>) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/targets/lab/capacity'), { target: 'lab', kind: 'docker-host', elastic: false, words: 'A fixed host: it does not grow.', readAt: '2026-10-13T02:00:00Z', ...c })
  return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><Capacity name="lab" /></QueryClientProvider></MemoryRouter>)
}

describe("a target's capacity", () => {
  it('shows a fixed host: what it has, what is promised and used, and its disk', () => {
    const html = render({
      memory: { total: fig(8 * 2 ** 30), promised: { sum: { apptown: fig(2 ** 30), other: fig(2 ** 28) }, limited: count(3, 1), unlimited: count(27, 2), unread: count(0, 0) }, inUse: { apptown: fig(2 ** 30), other: fig(2 ** 27) }, overcommitted: false },
      cpu: { total: fig(8), promised: { sum: { apptown: fig(4.5), other: fig(0) }, limited: count(5, 0), unlimited: count(25, 3), unread: count(0, 1) }, overcommitted: false },
      disk: { total: fig(719e9), free: fig(512e9), low: false, refuses: false },
    })
    expect(html).toContain('A fixed host: it does not grow.')
    expect(html).toContain('29 with no limit')
    expect(html).toContain('1 with a limit not read (the sum is partial)')
    expect(html).toContain('4.5 CPUs')
    expect(html).toContain('free of')
    expect(html).not.toContain('overcommitted</span>')
  })
  it('says unknown, never 0, for what it could not read, and flags overcommit and a low disk', () => {
    const html = render({
      memory: { total: fig(2 ** 30), promised: { sum: { apptown: fig(2 ** 31), other: fig(0) }, limited: count(2, 0), unlimited: count(0, 0), unread: count(0, 0) }, inUse: { apptown: none('docker stats: cannot connect'), other: none('docker stats: cannot connect') }, overcommitted: true },
      cpu: { total: none('docker info: cannot connect'), promised: { sum: { apptown: fig(0), other: fig(0) }, limited: count(0, 0), unlimited: count(0, 0), unread: count(0, 0) }, overcommitted: false },
      disk: { total: fig(100e9), free: fig(6e9), low: true, refuses: false, warning: 10 * 2 ** 30 },
    })
    expect(html).toContain('title="docker stats: cannot connect">unknown')
    expect(html).toContain('title="docker info: cannot connect">unknown')
    expect(html).toContain('overcommitted')
    expect(html).toContain('low: under 10.00 GB free')
  })
  // A host's disk says which filesystem it is and when it was read; below
  // its floor it says new releases are refused, and a disk it could not
  // read says why in words, not only on hover.
  it("says which disk it read, when, and when new releases are refused", () => {
    const refused = render({ disk: { total: fig(100 * 2 ** 30), free: fig(2 ** 30), low: true, refuses: true, floor: 5 * 2 ** 30, path: '/var/lib/docker', readAt: '2026-10-13T01:58:00Z' } })
    expect(refused).toContain("disk (Docker&#x27;s data, /var/lib/docker)")
    expect(refused).toContain('new releases refused: under 5.00 GB free')
    expect(refused).toMatch(/· read [^<]+ago/)
    const unread = render({ disk: { total: none('reading the disk: no space left on device'), free: none('reading the disk: no space left on device'), low: false, refuses: false, path: '/var/lib/docker' } })
    expect(unread).toContain('unknown <span class="muted">— reading the disk: no space left on device</span>')
    expect(unread).not.toContain('refused')
  })
  it('shows an elastic target as sizes and an estimate that says it is one', () => {
    const html = render({
      kind: 'do-app', elastic: true, words: 'Grows on demand: you pay for the size each part runs on.',
      parts: [
        { deployment: 'shop/cloud', part: 'web', runsOn: 'apps-s-1vcpu-1gb-fixed (1G, 1 CPU)', usdPerMonth: fig(10) },
        { deployment: 'shop/cloud', part: 'postgres', need: true, runsOn: 'db-s-1vcpu-1gb', usdPerMonth: none('the API lists no prices for managed databases') },
      ],
      estimate: { known: true, value: 10, reason: "an estimate of the app components' listed prices; managed databases are not included" },
    })
    expect(html).toContain('Grows on demand')
    expect(html).toContain('$10.00')
    expect(html).toContain('estimated')
    expect(html).toContain('managed databases are not included')
    expect(html).toContain('db-s-1vcpu-1gb')
  })

  // A Docker host that reports who holds it is drawn as bars, with the
  // same numbers in a table, a deployment linking to its page, and unknown
  // host use never called free.
  it('draws a host that reports its holders as bars with a table of the same numbers', () => {
    const memory = { total: fig(8 * 2 ** 30), promised: { sum: { apptown: fig(0), other: fig(0) }, limited: count(0, 0), unlimited: count(0, 0), unread: count(0, 0) }, hostUsed: none('not one machine'), overcommitted: false }
    const html = render({
      memory, cpu: { ...memory, total: fig(8), hostUsed: undefined },
      disk: { total: fig(400e9), free: fig(300e9), low: false, refuses: false },
      tenants: [{ kind: 'deployment', name: 'shop/main', project: 'shop', deployment: 'main', containers: 2, used: fig(2 ** 28), promised: fig(2 ** 29), unlimited: 1, cpus: 1, cpuUnlimited: 1, cpuUnread: 0 }],
    })
    expect(html).toContain('role="img"')
    expect(html).toContain('aria-label="memory used:')
    // The deployment's own segment links to it, not only its table row.
    expect(html).toMatch(/<a class="cap-seg cap-deployment"[^>]*href="\/projects\/shop\/settings\/deployments"/)
    expect(html).toContain('1 no limit')
    expect(html).toContain('free not known')
    expect(html).toContain('not one machine')
    const usedLabel = html.match(/aria-label="(memory used:[^"]*)"/)![1]
    expect(usedLabel).toContain('not read')
    expect(usedLabel).not.toMatch(/\bfree \d/)
    expect(html).toContain('the numbers')
    // A holder none of whose containers has a limit says so, never "0 B".
    const noLimit = render({
      memory, cpu: { ...memory, total: fig(8), hostUsed: undefined },
      tenants: [{ kind: 'other', name: 'other containers', containers: 3, used: fig(2 ** 20), promised: fig(0), unlimited: 3, cpus: 0, cpuUnlimited: 3, cpuUnread: 0 }],
    })
    expect(noLimit).toMatch(/other containers<\/td><td>3<\/td><td>[^<]+<\/td><td>no limit<\/td>/)
  })

  it('says a CPU promise is partial when a limit was not read, never 0 or unpromised', () => {
    const memory = { total: fig(8 * 2 ** 30), promised: { sum: { apptown: fig(0), other: fig(0) }, limited: count(0, 0), unlimited: count(0, 0), unread: count(0, 0) }, hostUsed: fig(2 ** 30), overcommitted: false }
    const html = render({
      memory, cpu: { ...memory, total: fig(8), hostUsed: undefined },
      tenants: [
        { kind: 'deployment', name: 'a/main', project: 'a', deployment: 'main', containers: 1, used: fig(2 ** 20), promised: fig(2 ** 28), unlimited: 0, cpus: 0, cpuUnlimited: 0, cpuUnread: 1 },
        { kind: 'deployment', name: 'b/main', project: 'b', deployment: 'main', containers: 1, used: fig(2 ** 20), promised: fig(2 ** 28), unlimited: 0, cpus: 2, cpuUnlimited: 0, cpuUnread: 1 },
      ],
    })
    expect(html).toContain('at least 2 CPUs of 8 CPUs · 2 limits not read')
    const cpuLabel = html.match(/aria-label="(CPUs promised:[^"]*)"/)![1]
    expect(cpuLabel).not.toContain('not promised')
    expect(cpuLabel).toContain('not read')
    expect(html).toMatch(/a\/main<\/a><\/td>(<td>[^<]*<\/td>){4}<td>1 not read<\/td>/)
    expect(html).toMatch(/b\/main<\/a><\/td>(<td>[^<]*<\/td>){4}<td>at least 2 CPUs \(1 not read\)<\/td>/)
  })
})

