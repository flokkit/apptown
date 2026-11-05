import { beforeEach, vi } from 'vitest'

// jsdom has no layout engine. Geometry is covered by the browser checks.
beforeEach(() => vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} }))
