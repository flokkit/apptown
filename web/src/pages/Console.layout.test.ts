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

it('keeps the bar to one line and every control a finger-sized target', () => {
  // The chat's bar is the page's bar (shell.css), one line wider than a
  // phone; on a phone its verbs take the line under the title, as every
  // page's actions do.
  expect(declarations('.page-bar:has(.chat-bar)')).toMatch(/flex-wrap:\s*nowrap/)
  const shell = (readFileSync('src/styles/shell.css', 'utf8') as string).replace(/\/\*[\s\S]*?\*\//g, '')
  expect([...shell.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find(r => r[1].trim() === '.page-bar')?.[2]).toMatch(/min-height:\s*44px/)
  // A 36 px button reaches 44 px through its outset target.
  expect(declarations('.chat-bar :is(.bar-btn, .bar-icon)')).toMatch(/min-height:\s*36px/)
  expect(declarations('.chat-bar :is(.bar-btn, .bar-icon)::after')).toMatch(/inset:\s*calc\(var\(--space-1\) \* -1\) 0/)
  expect(declarations('.composer > button')).toContain('min-height: 44px')
})

// The change page's live Conversation tab is one bounded column: only the
// conversation scrolls, so "at the end of the conversation" is what shows.
it('gives the change conversation the whole bounded page, with no outer scrolling', () => {
 const chain = '.ch-page-chat, .ch-page-chat .ch-tabs-sheet, .ch-page-chat .ch-tab-body'
 expect(declarations(chain)).toMatch(/flex:\s*1/)
 expect(declarations(chain)).toMatch(/min-height:\s*0/)
 expect(css).not.toMatch(/\.ch-page-chat[^{]*\{[^}]*overflow-y:\s*auto/)
 expect(css).not.toMatch(/min-height:\s*(auto|min-content)/)
 expect(declarations('.chat-scroll')).toMatch(/overflow-y:\s*auto/)
 expect(declarations('.conversation-split, .conversation-chat')).toMatch(/min-width:\s*0/)
 expect(declarations('.conversation-split.with-preview')).toMatch(/flex-direction:\s*row/)
 expect(declarations('.conversation-preview iframe')).toMatch(/min-height:\s*0/)
})
