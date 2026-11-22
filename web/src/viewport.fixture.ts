import { vi } from 'vitest'

// stubViewportWidth makes matchMedia answer width queries as a window this
// wide would: (max-width: N) and (width > N). Undo with vi.unstubAllGlobals.
export function stubViewportWidth(width: number) {
  vi.stubGlobal('matchMedia', (query: string) => {
    const max = query.match(/max-width:\s*(\d+)px/)
    const over = query.match(/width\s*>\s*(\d+)px/)
    const matches = max ? width <= Number(max[1]) : over ? width > Number(over[1]) : false
    return { matches, media: query, addEventListener() {}, removeEventListener() {} }
  })
}
