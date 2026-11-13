// @ts-expect-error -- this package has no Node types; only this read is used.
import { readFileSync } from 'node:fs'
import { expect, it } from 'vitest'

const css = (readFileSync('src/styles/console.css', 'utf8') as string).replace(/\/\*[\s\S]*?\*\//g, '')
const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
const declarations = (selector: string) => rules.filter(r => r[1].trim() === selector).map(r => r[2]).join('\n')

// This guards the sizing contract, not rendered geometry (which needs visual
// verification): the parent's remaining space sizes the console at all widths.
it('bounds the chat shell and gives the console remaining flex space without pixel deductions', () => {
  expect(declarations('.layout:has(.console-desk, .ch-page-chat)')).toMatch(/height:\s*100dvh/)
  expect(declarations('.layout:has(.console-desk, .ch-page-chat)')).toMatch(/min-height:\s*0/)
  expect(declarations('.main:has(.console-desk, .ch-page-chat)')).toMatch(/display:\s*flex/)
  expect(declarations('.main:has(.console-desk, .ch-page-chat)')).toMatch(/min-height:\s*0/)
  expect(declarations('.console')).toMatch(/flex:\s*1/)
  expect(declarations('.console')).toMatch(/min-height:\s*0/)
  expect(declarations('.console')).not.toMatch(/(?:^|[;\s])height\s*:/)
  expect(declarations('body:has(.console-desk, .ch-page-chat)')).toMatch(/position:\s*fixed/)
  expect(declarations('body:has(.console-desk, .ch-page-chat)')).toMatch(/overflow:\s*clip/)
})

it('wraps phone controls and keeps their touch targets', () => {
  expect(declarations('.console-head')).toContain('flex-wrap: wrap')
  expect(declarations('.console .workshop-actions button')).toContain('min-height: 44px')
  expect(declarations('.console .chat-input button')).toContain('min-height: 44px')
})

it('gives the change conversation the remaining space under its state panel and tabs', () => {
 expect(declarations('.ch-page-chat, .ch-page-chat .ch-tabs-sheet, .ch-page-chat .ch-tab-body')).toMatch(/flex:\s*1/)
 expect(declarations('.ch-page-chat, .ch-page-chat .ch-tabs-sheet, .ch-page-chat .ch-tab-body')).toMatch(/min-height:\s*0/)
 expect(declarations('.conversation-split, .conversation-chat')).toMatch(/min-width:\s*0/)
 expect(declarations('.conversation-split.with-preview')).toMatch(/flex-direction:\s*row/)
 expect(declarations('.conversation-preview iframe')).toMatch(/min-height:\s*0/)
})
