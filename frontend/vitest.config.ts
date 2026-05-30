import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

// First frontend test harness (v0.7.25). 58K lines of TS/TSX had zero tests;
// this seeds vitest against the PURE helpers that have actually regressed in
// production (unit formatters, the timeRangeToNs now()-trap, escapeHTML XSS,
// dashboard-variable expansion) — mirroring CLAUDE.md #11's "regression test
// for bug-fixes" discipline on the frontend side.
//
// Node environment on purpose: the seeded tests cover side-effect-free lib/
// functions, so no jsdom. When the first component/hook test lands (render
// traps, URL-state sync), add a jsdom project rather than flipping the global
// env — most of the value is in fast, DOM-free pure-function tests.
export default defineConfig({
  // Mirror the Vite `@` → src alias so tests can import components that use it
  // (e.g. ServicePicker imports '@/lib/api').
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    // Surface slow tests early; pure helpers should be sub-millisecond.
    slowTestThreshold: 50,
  },
});
