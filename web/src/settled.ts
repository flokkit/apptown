import { act } from 'react'
import type { QueryClient } from '@tanstack/react-query'

// settled is for page tests. It waits, without a clock of its own, until the
// test's query client has no read in flight and React has rendered every
// answer, including reads an answer starts (a read enabled by another's
// answer, a refresh an effect asks for). A read that is retrying is still in
// flight: a test of a failing read advances a fake clock through the retry
// schedule instead.
//
// Each pass ends its act() before looking: the client hands answers to React
// on a zero-delay tick, and only when act() returns has React rendered them
// and run the effects that may start another read. A read can also start and
// answer during that closing render, leaving its answer for the next tick.
// So settled returns only after a quiet pass: nothing in flight when it
// began, and nothing changed in the client's cache while it ran (which
// covers both a read started and an answer delivered). Reads that keep
// starting each other fail after `passes` passes rather than at the test's
// timeout.
export async function settled(client: QueryClient, passes = 50): Promise<void> {
  for (let pass = 0; pass < passes; pass++) {
    let changed = client.isFetching() > 0
    const off = client.getQueryCache().subscribe(() => { changed = true })
    try {
      await act(async () => {
        if (client.isFetching() > 0) {
          await new Promise<void>((resolve) => {
            const done = client.getQueryCache().subscribe(() => {
              if (client.isFetching() === 0) { done(); resolve() }
            })
          })
        }
        await new Promise((r) => setTimeout(r, 0))
      })
    } finally {
      off()
    }
    if (!changed && client.isFetching() === 0) return
  }
  throw new Error(`settled: reads were still starting after ${passes} passes`)
}
