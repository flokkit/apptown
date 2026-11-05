import assert from 'node:assert/strict'
import test from 'node:test'
import stylelint from 'stylelint'
import config from './stylelint.config.mjs'
import { checkDebt } from './css-check.mjs'
const lint = async (code, name = 'src/fixture.css') => (await stylelint.lint({
  code, codeFilename: new URL(name, import.meta.url).pathname,
  config, configBasedir: new URL('.', import.meta.url).pathname,
})).results[0].warnings.map(w => w.rule)

test('new stylesheet drift is refused by the real configuration', async () => {
  for (const [css, rule] of [
    ['#new { color: var(--text) }', 'selector-max-id'],
    ['.a.b.c.d.e.f { color: var(--text) }', 'selector-max-specificity'],
    ['.new { color: red }', 'apptown/tokens'],
    ['.new { background: linear-gradient(#fff, var(--bg)) }', 'apptown/tokens'],
    ['.new { --local: rgb(0 0 0) }', 'apptown/tokens'],
    ['.new { color: var(--text, #fff) }', 'apptown/tokens'],
    ['.new { border: 1px solid blue }', 'apptown/tokens'],
    ['.new { border-radius: 4px }', 'apptown/tokens'],
    ['.new { font-size: 1rem }', 'apptown/tokens'],
    ['.new { font: 1rem sans-serif }', 'apptown/tokens'],
    ['.new { font: small sans-serif }', 'apptown/tokens'],
    ['.new { z-index: 7 }', 'apptown/tokens'],
    ['.new { margin: 8px !important }', 'declaration-no-important'],
    ['.new { padding: calc(8px + var(--space-1)) }', 'apptown/pixel-spacing'],
    ['@media (900px > width) { .new { display: block } }', 'apptown/breakpoints'],
    ['@media (max-width: 700px) { .new { display: block } }', 'apptown/breakpoints'],
  ]) assert.ok((await lint(css)).includes(rule), css)
})

test('tokens and named exceptions pass', async () => {
  assert.deepEqual(await lint(`.new { color: currentColor; background: transparent;
    border: 1px solid var(--rule); border-radius: 50%; font-size: inherit;
    font: var(--fs-base) var(--font-sans); z-index: auto; gap: var(--space-2); margin: 0 auto; top: 1px;
    padding: calc(100vw - 32px); padding-bottom: calc(env(safe-area-inset-bottom) + 16px);
    border-spacing: 1px; border-radius: 999px; }
    @media (max-width: 600px) { .new { color: var(--text) } }
    @media (width > 800px) { .new { color: var(--text) } }`), [])
  assert.deepEqual(await lint(':root { --color: #fff; --radius: 4px; --z-base: 1 }', 'src/styles/tokens.css'), [])
})

test('debt is per file and per rule, and reductions must tighten the baseline', () => {
  const debt = { 'web/src/old.css': { 'declaration-no-important': 1, 'apptown/pixel-spacing': 2 } }
  assert.deepEqual(checkDebt(debt, debt), [])
  assert.equal(checkDebt({}, debt).length, 2)
  assert.equal(checkDebt(debt, {}).length, 2)
  assert.equal(checkDebt({ 'web/src/new.css': debt['web/src/old.css'] }, debt).length, 4)
})
