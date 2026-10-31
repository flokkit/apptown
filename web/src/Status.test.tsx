import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
// @ts-expect-error -- Node is used only to read the generated contract.
import { readFileSync, readdirSync } from 'node:fs'
import { STATUS_LIGHT, Status } from './Status'

// A newly added server display label must have a light before it can ship.
// Read the generated unions so this test cannot silently omit a new enum member.
describe('server display vocabulary', () => {
  const wire = readFileSync('src/wire.gen.ts', 'utf8') as string
  const labels = new Set([...wire.matchAll(/\b(?:statusLabel|stateLabel|copyLabel|readinessLabel|restoreLabel|overrideLabel|mergeLabel|observation): (\w+)/g)].map(m => m[1]).filter(name => name !== 'string'))
  labels.add('DepartureLabel') // The departures contract names this field "state".
  for (const name of labels) {
    it(`maps every ${name} label to a vocabulary light`, () => {
      const declaration = wire.match(new RegExp(`export type ${name} = ([^\\n]+)`))?.[1]
      expect(declaration).toBeTruthy()
      for (const [, word] of declaration!.matchAll(/"([^"]+)"/g)) {
        expect(STATUS_LIGHT).toHaveProperty(word)
        expect(['pass', 'running', 'pending', 'fail', 'unknown', 'overridden', '']).toContain(STATUS_LIGHT[word as keyof typeof STATUS_LIGHT])
      }
    })
  }
  it('keeps the state readable and its reason in help', () => {
    const html = renderToStaticMarkup(<Status word="failed" tone="fail" help="Tests did not pass." />)
    expect(html).toContain('aria-hidden="true">● </span>failed')
    expect(html).toContain('<details class="status-help"><summary>')
    expect(html).not.toContain('role="status"')
    expect(html).toContain('title="Tests did not pass."')
    expect(html).toContain('aria-label="failed: Tests did not pass."')
  })
})

it('draws status lights only in the shared Status component', () => {
 const files = readdirSync('src', {recursive: true}) as string[]
 for (const file of files.filter(f => f.endsWith('.tsx') && !f.includes('.test.') && f !== 'Status.tsx')) {
  expect(readFileSync(`src/${file}`, 'utf8'), file).not.toContain('●')
 }
})

it('keeps help out of table layout and resets the global disclosure box', () => {
 const css = readFileSync('src/styles.css', 'utf8') as string
 expect(css).toMatch(/\.status-help\s*\{[^}]*margin: 0/)
 expect(css).toMatch(/\.status-help > summary\s*\{[^}]*padding: 0; border: 0; background: none/)
 expect(css).toMatch(/\.status-help\[open\] > summary::before\s*\{ content: none/)
 expect(css).toMatch(/\.status-help > div\s*\{[^}]*position: absolute/)
 const project = readFileSync('src/pages/Project.tsx', 'utf8') as string
 expect(project.slice(project.indexOf('function ProjectHeader')).match(/<h1>[\s\S]*?<\/h1>/)?.[0]).not.toContain('RuntimeStatus')
})

it.each(['none', 'not set up'] as const)('keeps %s neutral without a light', word => {
 const html = renderToStaticMarkup(<Status word={word} tone="" />)
 expect(html).toContain(word)
 expect(html).not.toContain('aria-hidden')
})
