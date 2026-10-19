import { describe, expect, it } from 'vitest'
import { PRODUCT } from './brand'

// The product's name on every page comes from PRODUCT (brand.ts), so a page
// never says the old code name "forge" or a copy of the name that a later
// change to PRODUCT would leave behind. This reads source lines, so it claims
// no more than that: a name built from pieces, or one that arrives from the
// server, gets past it.

// Code names that stay "forge" because a person types them or a program reads
// them; each is removed before the word check.
const codeNames: [RegExp, string][] = [
  [/X-Forge-[A-Za-z-]+/g, 'request headers'],
  [/FORGE_[A-Z_]*/g, 'settings and constants'],
  [/\.forge\/|\/_forge\/|refs\/forge\//g, 'folders, paths and refs'],
  [/forge:[a-z:-]+|\{forge:/g, 'token scopes and link placeholders'],
  [/\[forge check\]/g, 'the chat marker a check sends'],
  [/['"`]forge[.-][a-z.-]+/g, 'storage keys and release names'],
  [/<code>[^<]*<\/code>|\bforge (check|dev|test|token|wait|show|approve|executing|version)\b/g, 'the forge command and its subcommands'],
  [/\b[A-Za-z_]+\.forge\b|[{,]\s*forge:\s|\bforge\?/g, 'identifiers: p.forge, an object key such as { forge: PRODUCT }'],
  [/['"]forge['"]\s*(\)|\||,|\])|[!=]==?\s*['"]forge['"]/g, "data values compared or typed: about 'forge', capacity kind 'forge'"],
  [/apptown|forge\.flokkit\.io|[a-z]-forge\b/g, 'file names, addresses, the workflow apiVersion and keys such as chat-on-forge'],
]

function strayNames(file: string, src: string): string[] {
  const out: string[] = []
  // Block comments may span lines; blank them but keep the line numbers.
  const code = src.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, ' '))
  code.split('\n').forEach((raw, i) => {
    const t = raw.trim()
    if (t.startsWith('//')) return
    let line = raw.replace(/\/\/ .*$/, '')
    for (const [re] of codeNames) line = line.replace(re, '')
    if (/\b[Ff]orge\b/.test(line)) out.push(`${file}:${i + 1}: says "forge": ${t}`)
    if (file !== './brand.ts' && line.includes('AppTown')) out.push(`${file}:${i + 1}: a literal "AppTown", not PRODUCT: ${t}`)
  })
  return out
}

const sources = import.meta.glob(['./**/*.ts', './**/*.tsx', '!./**/*.test.ts', '!./**/*.test.tsx', '!./wire.gen.ts'],
  { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('the product name', () => {
  it('comes from PRODUCT on every page', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50)
    const found = Object.entries(sources).flatMap(([f, src]) => strayNames(f, src))
    expect(found).toEqual([])
  })

  it('catches the old name in prose, lowercase or not, and a copied name', () => {
    expect(strayNames('./x.tsx', "<div>The forge could not say who you are</div>")).toHaveLength(1)
    expect(strayNames('./x.tsx', "return `forge copies: ${last}`")).toHaveLength(1)
    expect(strayNames('./x.tsx', '<span className="chip">forge runs it</span>')).toHaveLength(1)
    expect(strayNames('./x.tsx', '<div>Sign in to AppTown</div>')).toHaveLength(1)
  })

  it('leaves code names alone', () => {
    for (const line of [
      "headers: { 'X-Forge-Actor': currentUser() }",
      "const KEY = 'forge-theme'",
      "localStorage.getItem('forge.user')",
      "d.scopes.includes('forge:user:read')",
      "x.text.startsWith('[forge check]')",
      'Run the full checks first (forge check), fix',
      '<code>git fetch the.bundle refs/forge/refused/chat-1</code>',
      "{c.about && c.about !== 'forge' && <span/>}",
      "export type Kind = 'deployment' | 'forge' | 'other'",
      '<FigureWords f={p.forge} />',
      `<b>{${'PRODUCT'}}</b>`,
    ]) expect(strayNames('./x.tsx', line)).toEqual([])
    expect(PRODUCT).toBe('AppTown')
  })
})

// AP-C01: prose beside code and display values are not protocol names.
describe('product prose is not a code-name exception', () => {
  it('checks an installation name followed by a colon', () => {
    expect(strayNames('./x.tsx', "From another forge: its project's Repository › Download code. Or make one with <code>git bundle create code.bundle main --tags</code>.")).toHaveLength(1)
  })
  it('checks a displayed name after a stored-value comparison', () => {
    expect(strayNames('./x.tsx', "{p.memoryDefault.kind === 'forge' ? 'forge' : p.memoryDefault.kind} default")).toHaveLength(1)
  })
})
