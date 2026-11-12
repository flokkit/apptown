// @ts-expect-error -- this package has no Node types; only this read is used.
import { readFileSync } from 'node:fs'
import wizard from '../NewProject.tsx?raw'
import { expect, it } from 'vitest'

const css = readFileSync('src/styles/forms.css', 'utf8') as string

it('keeps visual counters silent and names completion once', () => {
 expect(css).toContain('content: counter(wiz) / ""')
 expect(css).toContain('content: "✓" / ""')
 expect(wizard).toContain('<span className="sr-only"> completed</span>')
 expect(wizard).toContain('aria-current={step === i + 1 ? \'step\' : undefined}')
})
