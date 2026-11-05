import stylelint from 'stylelint'
import valueParser from 'postcss-value-parser'
import colorNames from 'color-name'

// A var() fallback is still a literal; inspect it as well as the outer value.
function literals(value) {
  const words = []
  valueParser(value).walk(node => {
    if (node.type === 'word' && !node.value.startsWith('--')) words.push(node.value)
    if (node.type === 'function' && /^(?:rgb|hsl|hwb|lab|lch|oklab|oklch|color)/i.test(node.value)) words.push(node.value + '(')
    if (node.type === 'function' && /^(?:url|env)$/.test(node.value)) return false
  })
  return words
}
const allowed = /^(?:0|auto|inherit|currentcolor|transparent)$/i
function plugin(name, check) {
  const rule = () => (root, result) => check(root, (node, message) => stylelint.utils.report({ ruleName: name, result, node, message }))
  rule.ruleName = name
  return stylelint.createPlugin(name, rule)
}
export default [
  plugin('apptown/tokens', (root, report) => {
    if (root.source.input.file?.endsWith('/styles/tokens.css')) return
    root.walkDecls(decl => {
      const prop = decl.prop.toLowerCase()
      // Defining a local variable must not hide hardcoded colour or depth.
      const words = literals(decl.value)
      const shape = /radius$/.test(prop)
      const typeOrDepth = /^(?:font-size|z-index)$/.test(prop)
      const bad = words.some(word => {
        if (allowed.test(word)) return false
        if (shape) return !/^(?:50%|999px)$/.test(word)
        if (typeOrDepth) return true
        if (prop === 'font' && /^(?:-?(?:\d*\.)?\d+(?:[a-z]+|%)|xx?-small|small|medium|large|xx?-large|smaller|larger)$/i.test(word)) return true
        return /^#[\da-f]{3,8}$/i.test(word) || /^(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(/i.test(word) || Object.hasOwn(colorNames, word.toLowerCase())
      })
      if (bad) report(decl, `${prop}: use a custom property from styles/tokens.css`)
    })
  }),
  plugin('apptown/pixel-spacing', (root, report) => {
    if (root.source.input.file?.endsWith('/styles/tokens.css')) return
    root.walkDecls(decl => {
      if (!/^(?:(?:margin|padding|gap|inset|scroll-margin|scroll-padding)(?:-.+)?|row-gap|column-gap|top|right|bottom|left|border-spacing|text-indent)$/.test(decl.prop)) return
      let pixels = false
      valueParser(decl.value).walk(node => {
        // Viewport and safe-area arithmetic must keep a fixed edge allowance.
        if (node.type === 'function' && /^(?:calc|min|max|clamp)$/.test(node.value)
          && /(?:[0-9](?:d?v[wh]|sv[wh]|lv[wh])\b|env\(safe-area-inset-)/.test(valueParser.stringify(node))) return false
        if (node.type === 'word' && /^-?(?:\d*\.)?\d+px$/i.test(node.value) && Math.abs(parseFloat(node.value)) > 1) pixels = true
      })
      if (pixels) report(decl, 'Use --space-* for spacing instead of pixels')
    })
  }),
  plugin('apptown/breakpoints', (root, report) => {
    root.walkAtRules('media', node => {
      // The lower bound is exclusive, so desktop starts immediately above 800.
      const expressions = node.params.match(/\([^()]*\bwidth[^()]*\)/gi) || []
      const step = '(?:600|800)px'
      const valid = new RegExp(`^\\(\\s*(?:(?:min-|max-)?width\\s*:\\s*${step}|width\\s*[<>=]{1,2}\\s*${step}|${step}\\s*[<>=]{1,2}\\s*width(?:\\s*[<>=]{1,2}\\s*${step})?)\\s*\\)$`, 'i')
      if ((node.params.match(/\bwidth\b/gi) || []).length !== expressions.length || expressions.some(value => !valid.test(value))) {
        report(node, 'Use only the 600px or 800px breakpoint')
      }
    })
  }),
]
