import { defineConfig, globalIgnores } from 'eslint/config';
import nextVitals from 'eslint-config-next/core-web-vitals';
import nextTs from 'eslint-config-next/typescript';

export default defineConfig([
  ...nextVitals,
  ...nextTs,
  globalIgnores(['.next/**', 'out/**', 'next-env.d.ts', 'test-results/**', 'playwright-report/**']),
  {
    rules: {
      // Styling lives in CSS Modules and design tokens; inline styles would also need a CSP exception.
      'react/forbid-dom-props': ['error', { forbid: [{ propName: 'style', message: 'Use a CSS Module class instead of inline styles.' }] }],
      'react/forbid-component-props': ['error', { forbid: [{ propName: 'style', message: 'Use a CSS Module class instead of inline styles.' }] }],
      // No raw HTML injection: it bypasses React's escaping and the CSP hashes.
      'react/no-danger': 'error',
      // Every visible string goes through the message catalogs.
      'react/jsx-no-literals': ['error', { noStrings: false, allowedStrings: ['·', '—', '/', ':', '…', '(', ')'] }],
      'no-restricted-syntax': [
        'error',
        { selector: "JSXAttribute[name.name='style']", message: 'Use a CSS Module class instead of inline styles.' },
        { selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']", message: 'Raw HTML is not allowed.' },
      ],
      '@typescript-eslint/consistent-type-imports': ['error', { fixStyle: 'inline-type-imports' }],
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    },
  },
  {
    // Tests and tooling may use literal strings.
    files: ['**/*.test.{ts,tsx}', 'e2e/**', 'scripts/**', '*.config.*'],
    rules: { 'react/jsx-no-literals': 'off' },
  },
  {
    // Playwright fixtures receive a `use` callback, which is not React's use() hook.
    files: ['e2e/**'],
    rules: { 'react-hooks/rules-of-hooks': 'off' },
  },
]);
