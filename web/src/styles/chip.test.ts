// @ts-expect-error -- this package has no Node types; only this read is used.
import { readFileSync } from 'node:fs'
import { expect, it } from 'vitest'
import { STATUS_LIGHT } from '../Status'

const css = (readFileSync('src/styles/chip.css', 'utf8') as string).replace(/\/\*[\s\S]*?\*\//g, '')
const declarations = (selector: string) => [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter(r => r[1].trim() === selector).map(r => r[2]).join('\n')

// A neutral state such as "stopped" has no tone of its own, so its stamp is
// the plain chip. It is set in the secondary text colour, which both themes
// define, so it reads beside the coloured states; the fainter meta colour
// read as disabled on the dark panel.
it('draws a stopped status in the secondary text colour', () => {
  expect(STATUS_LIGHT.stopped).toBe('')
  expect(declarations('.chip')).toMatch(/(?:^|[;\s])color:\s*var\(--muted\)/)
})
