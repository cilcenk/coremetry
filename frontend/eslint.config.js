import js from '@eslint/js';
import globals from 'globals';
import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';

// Advisory-first ESLint (v0.7.49) — the frontend counterpart to the
// golangci-lint umbrella (v0.7.26). The CI lint step runs it non-blocking over
// the existing baseline; the noisiest rules are softened to 'warn'/'off' so the
// signal is the load-bearing stuff (rules-of-hooks, unreachable code, real
// unused bindings) rather than a wall of style nits. Flip the CI step to
// blocking once the baseline is triaged.
export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'src/**/*.test.ts', '*.config.js', '*.config.ts'] },
  {
    files: ['src/**/*.{ts,tsx}'],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser },
    },
    // React rules set explicitly (not via the plugin's preset) so this config
    // is robust across react-hooks/react-refresh plugin versions.
    plugins: { 'react-hooks': reactHooks, 'react-refresh': reactRefresh },
    rules: {
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      // Softened for the advisory baseline — intentional patterns in this
      // codebase (operator-controlled `any` at narrow OTel/attr boundaries,
      // empty catch on best-effort fetches, leading-underscore throwaways).
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      'no-empty': ['warn', { allowEmptyCatch: true }],
      'prefer-const': 'warn',
    },
  },
);
