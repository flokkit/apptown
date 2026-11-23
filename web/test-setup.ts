// jsdom has no layout engine and no ResizeObserver. Geometry is covered by
// the browser checks; components that watch sizes get an observer that never
// fires. It is defined once, not stubbed per test, so a test that restores
// its own stubbed globals mid-test (vi.unstubAllGlobals) keeps it.
class NoLayoutResizeObserver { observe() {} unobserve() {} disconnect() {} }
;(globalThis as { ResizeObserver?: unknown }).ResizeObserver ??= NoLayoutResizeObserver
