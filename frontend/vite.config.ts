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
        // v0.9.708 — manualChunks(fn) → advancedChunks: Vite 8 rolldown,
        // CJS facade modüllerinde (react'in require şimleri gibi) fonksiyon
        // formunu UYGULAMIYOR: @grafana pilotu gelince rolldown react'in
        // CJS instance'ını grafana chunk'ına taşıdı ve HER sayfa
        // require_react/require_jsx_runtime'ı oradan statik import etti —
        // 178 KB gzip'lik lazy chunk index.html modulepreload'ına bindi
        // (unminified build + sourcemap ile teşhis). advancedChunks
        // rolldown'ın birinci sınıf mekanizması; eski kurallar birebir
        // çevrildi, react AÇIK yüksek öncelikle vendor'da.
        // Kural gerekçeleri (router/tanstack/charts/otel/graph) için git
        // geçmişindeki manualChunks yorumlarına bak — v0.8.477/514.
        advancedChunks: {
          groups: [
            // Sanal modüller (\0-önekli): Vite preload yardımcısı + CJS
            // interop. Grupsuz kalınca rolldown ilk dinamik-import edene
            // (grafana) kümeliyor ve HER chunk oradan import ediyor —
            // __vitePreload vakası, unminified build ile teşhis.
            { name: 'vendor', test: /^\u0000|vite\/(preload-helper|modulepreload)|commonjsHelpers/, priority: 110 },
            { name: 'vendor', test: /node_modules\/(react|react-dom|scheduler)[\/?]/, priority: 100 },
            { name: 'grafana', test: /node_modules\/@grafana\//, priority: 90 },
            { name: 'router', test: /node_modules\/react-router/, priority: 80 },
            { name: 'tanstack', test: /node_modules\/@tanstack\/(query-core|react-query)/, priority: 80 },
            // charts > grafana önceliği ŞART: rolldown yüksek öncelikli grup
            // chunk'ı kurulurken henüz sahiplenilmemiş bağımlılıkları YUTUYOR
            // (docs: "modules of that group will be removed from other
            // groups"). 80'deyken uplot grafana'ya yutuldu ve dört eager
            // preset lazy chunk'a statik kenar açtı. react'in 100 pini de
            // aynı mekanizmanın çözümüydü.
            { name: 'charts', test: /node_modules\/uplot/, priority: 95 },
            { name: 'otel', test: /node_modules\/(@opentelemetry|zone\.js)/, priority: 80 },
            { name: 'graph', test: /node_modules\/(dagre|graphlib|lodash)/, priority: 80 },
            { name: 'vendor', test: /node_modules/, priority: 10 },
          ],
        },
      },
    },
  },
});
