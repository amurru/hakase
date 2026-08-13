import path from 'node:path'
import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  base: './',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Fonts must ship as real files, not data: URIs: KaTeX_Size3 is small
    // enough to fall under the default 4096-byte inline limit and the CSP
    // header (font-src 'self') blocks data: fonts.
    assetsInlineLimit: (filePath: string) => !/\.(woff2?|ttf|otf|eot)$/i.test(filePath),
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
