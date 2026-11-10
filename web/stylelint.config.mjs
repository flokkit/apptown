export default {
  plugins: ['./stylelint-rules.mjs'],
  reportNeedlessDisables: true,
  rules: {
    'declaration-no-important': true,
    'apptown/pixel-spacing': true,
    'selector-max-id': 0,
    'selector-max-specificity': '0,5,3',
    'apptown/tokens': true,
    'apptown/type-values': true,
    'apptown/breakpoints': true,
  },
}
