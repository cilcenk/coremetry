import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { visualizer } from 'rollup-plugin-visualizer';
import path from 'path';

// Vite config — pure SPA build, output to dist/ which the Go
// binary embeds via //go:embed all:frontend/dist. Dev-only API
// proxy mirrors the Next.js rewrite that pointed /api at the Go
// backend on localhost:8088 (production has the same origin).
// ANALYZE=1 npm run build → renders dist/bundle-analysis.html
// with a treemap of every chunk + raw / gzip sizes per file.
// The default sunburst view is more compact than treemap for
// our chunk sizes; switch by changing template below.
const analyze = process.env.ANALYZE === '1' || process.env.ANALYZE === 'true';

export default defineConfig({
  plugins: [
    react(),
    ...(analyze ? [
      visualizer({
        filename: 'dist/bundle-analysis.html',
        template: 'treemap',
        gzipSize: true,
        brotliSize: true,
        open: false,
      }),
    ] : []),
  ],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: {
    port: 4888,
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
            // v0.8.514 (perf raporu #17) — kural daraltıldı: react-virtual
            // yalnız VirtualList/VirtualTable tüketiyor; geniş '@tanstack'
            // eşleşmesi onu da eager 'tanstack' chunk'ına itiyordu. Artık
            // yalnız query çekirdeği eager; virtual, kullanan route
            // chunk'larına taşınır (Rollup default'u).
            if (id.includes('@tanstack/query-core') || id.includes('@tanstack/react-query')) return 'tanstack';
            if (id.includes('uplot')) return 'charts';
            if (id.includes('@opentelemetry')) return 'otel';
            // v0.8.477 (perf dalga-2) — zone.js yalnız browserOtel'in
            // ZoneContextManager'ı için var (main.tsx'te idle dinamik
            // import); catch-all onu HER soğuk açılışta inen vendor'a
            // sabitliyordu (~37KB min / ~11KB gz — vendor'ın %17.5'i).
            // otel chunk'ına taşınınca RUM ile birlikte tembel iner.
            if (id.includes('zone.js')) return 'otel';
            // dagre (+ its graphlib + lodash layout deps) is a heavy
            // layered-DAG engine imported ONLY by ServiceGraph.tsx,
            // which is reached solely through the route-lazy
            // ServiceGraphPreview page. Without this rule the vendor
            // catch-all below pins dagre into the ALWAYS-loaded vendor
            // chunk, so every cold first paint ships ~40 kB gz of graph
            // layout the operator may never open. Splitting it into its
            // own 'graph' chunk lets it load only with the topology
            // route. graphlib + lodash are dagre-exclusive transitive
            // deps (no app source imports lodash), so co-locating them
            // keeps the chunk self-contained AND strips the lodash shard
            // out of vendor too.
            if (
              id.includes('/dagre/') ||
              id.includes('/graphlib/') ||
              id.includes('/lodash')
            ) return 'graph';
            return 'vendor';
          }
        },
      },
    },
  },
});
