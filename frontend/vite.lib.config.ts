// vite.lib.config.ts — v0.10.156 (design-sync): `components/ui` atomlarını
// tek ES modülü + .d.ts ağacı olarak `dist-ds/`'e derler. Uygulama build'i
// (vite.config.ts) DEĞİŞMEZ; bu yalnız Claude Design senkronunun (design-sync)
// tükettiği kütüphane çıktısı: `npm run build:ds`. React/router/ikon paketleri
// dış bağımlılık kalır (bundle'a gömülmez).
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': path.resolve(__dirname, 'src') } },
  build: {
    outDir: 'dist-ds',
    emptyOutDir: true,
    copyPublicDir: false,
    sourcemap: false,
    minify: false,
    lib: {
      entry: path.resolve(__dirname, 'src/components/ui/index.ts'),
      formats: ['es'],
      fileName: () => 'index.es.js',
    },
    rollupOptions: {
      external: [
        'react', 'react-dom', 'react/jsx-runtime', 'react-dom/client',
        'react-router-dom', 'lucide-react', '@tanstack/react-virtual', 'uplot',
        '@tanstack/react-query',
      ],
    },
  },
});
