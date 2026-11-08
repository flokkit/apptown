import { ESLint } from 'eslint'
import { expect, it } from 'vitest'

// The production lint configuration must refuse every native browser dialog,
// including bare calls (the historical Secrets alert) and computed properties.
// Linting loads the whole ESLint configuration, which takes seconds on a busy
// machine; the gate must not fail on load alone, so this test has more time.
it('keeps native browser dialogs at zero in web source', { timeout: 30_000 }, async () => {
  const lint = new ESLint()
  for (const name of ['prompt', 'confirm', 'alert']) {
    for (const call of [`window.${name}('x')`, `window['${name}']('x')`, `globalThis.${name}('x')`, `${name}('x')`]) {
      const [result] = await lint.lintText(call, { filePath: 'src/dialog-probe.tsx' })
      expect(result.messages.some(m => m.ruleId?.startsWith('no-restricted-')), call).toBe(true)
    }
  }
})
