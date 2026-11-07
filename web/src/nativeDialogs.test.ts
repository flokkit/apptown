import { ESLint } from 'eslint'
import { expect, it } from 'vitest'

// The production lint configuration must refuse every native browser dialog,
// including bare calls (the historical Secrets alert) and computed properties.
it('keeps native browser dialogs at zero in web source', async () => {
  const lint = new ESLint()
  for (const name of ['prompt', 'confirm', 'alert']) {
    for (const call of [`window.${name}('x')`, `window['${name}']('x')`, `globalThis.${name}('x')`, `${name}('x')`]) {
      const [result] = await lint.lintText(call, { filePath: 'src/dialog-probe.tsx' })
      expect(result.messages.some(m => m.ruleId?.startsWith('no-restricted-')), call).toBe(true)
    }
  }
})
