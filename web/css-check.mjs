import stylelint from 'stylelint'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { relative } from 'node:path'
import config from './stylelint.config.mjs'

export const ratchets = ['declaration-no-important', 'apptown/pixel-spacing']
export function checkDebt(actual, baseline) {
  const errors = []
  for (const file of new Set([...Object.keys(actual), ...Object.keys(baseline)])) {
    for (const rule of ratchets) {
      const count = actual[file]?.[rule] || 0
      const limit = baseline[file]?.[rule] || 0
      if (count > limit) errors.push(`${file}: ${rule}: ${count} exceeds ${limit}; use tokens and the existing cascade`)
      if (count < limit) errors.push(`${file}: ${rule}: lower the baseline from ${limit} to ${count}`)
    }
  }
  return errors
}
export async function inspect() {
  const root = fileURLToPath(new URL('../', import.meta.url))
  const lint = await stylelint.lint({ files: [root + 'web/src/**/*.css'], config, configBasedir: fileURLToPath(new URL('.', import.meta.url)) })
  const actual = {}, errors = []
  for (const result of lint.results) {
    const file = relative(root, result.source)
    for (const warning of result.warnings) {
      if (ratchets.includes(warning.rule)) {
        actual[file] ||= {}
        actual[file][warning.rule] = (actual[file][warning.rule] || 0) + 1
      } else errors.push(`${file}:${warning.line}:${warning.column}: ${warning.text}`)
    }
    for (const error of result.invalidOptionWarnings) errors.push(error.text)
  }
  return { actual, errors }
}
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const { actual, errors } = await inspect()
  const baseline = JSON.parse(readFileSync(new URL('./css-baseline.json', import.meta.url), 'utf8'))
  errors.push(...checkDebt(actual, baseline))
  console.log(errors.length ? errors.join('\n') : 'CSS: tokens, selectors, breakpoints and per-file debt passed')
  process.exitCode = errors.length ? 1 : 0
}
