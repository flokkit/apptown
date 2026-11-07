// Minimal lint: TypeScript's recommended set, the two React hook rules the
// code already refers to in eslint-disable comments, and static-components:
// a component defined inside another's render is a new type every render,
// so each refresh remounts it and drops its state (open folds on a polled
// page closed on every poll).
import tseslint from 'typescript-eslint'
import reactHooks from 'eslint-plugin-react-hooks'

export default tseslint.config(
  { ignores: ['dist/**', 'node_modules/**'] },
  ...tseslint.configs.recommended,
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: { 'react-hooks': reactHooks },
    rules: {
      'no-restricted-globals': ['error', 'prompt', 'confirm', 'alert'],
      'no-restricted-properties': ['error', ...['window', 'globalThis', 'self'].flatMap(object =>
        ['prompt', 'confirm', 'alert'].map(property => ({ object, property, message: 'Use an in-page form, QuietConfirm, or inline error.' })))],
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
      'react-hooks/static-components': 'error',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      '@typescript-eslint/no-non-null-assertion': 'off',
    },
  },
)
