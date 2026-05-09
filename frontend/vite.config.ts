import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'path';

// Vite config — pure SPA build, output to dist/ which the Go
// binary embeds via //go:embed all:frontend/dist. Dev-only API
// proxy mirrors the Next.js rewrite that pointed /api at the Go
// backend on localhost:8088 (production has the same origin).
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: {
    port: 3000,
    proxy: {
      '/api': 'http://localhost:8088',
      '/v1': 'http://localhost:8088',
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        // Keep chunk filenames stable so Cloudflare / browser
        // caches don't churn on every release just because the
        // chunk hash bumped for an unrelated reason. Vite's
        // default already content-hashes, this just opts the
        // chunk strategy into "by-import" granularity.
        manualChunks(id) {
          if (id.includes('node_modules')) {
            if (id.includes('react-router')) return 'router';
            if (id.includes('@tanstack')) return 'tanstack';
            if (id.includes('uplot')) return 'charts';
            return 'vendor';
          }
        },
      },
    },
  },
});
