import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'

export default defineConfig({
  plugins: [preact()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    target: 'es2022',
    // two pages: the panel (index.html) and the status page / users' page (status/index.html)
    rollupOptions: {
      input: {
        panel: fileURLToPath(new URL('./index.html', import.meta.url)),
        status: fileURLToPath(new URL('./status/index.html', import.meta.url)),
      },
    },
  },
  server: {
    port: 5173,
    // the panel the dev pages talk to (MERIDIAN_PANEL, e.g. a local demo panel on another port)
    proxy: Object.fromEntries(['/api', '/s/', '/agent', '/brand'].map((p) => [p, process.env.MERIDIAN_PANEL || 'http://127.0.0.1:18080'])),
  },
})
