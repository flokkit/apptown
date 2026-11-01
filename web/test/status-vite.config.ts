import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

if (!process.env.STATUS_FIXTURE_OUT) throw new Error('Set STATUS_FIXTURE_OUT to a temporary output directory')
export default defineConfig({
  root: fileURLToPath(new URL('..', import.meta.url)),
  plugins: [react()],
  build: {
    outDir: process.env.STATUS_FIXTURE_OUT,
    emptyOutDir: true,
    rollupOptions: { input: fileURLToPath(new URL('./status-fixture.html', import.meta.url)) },
  },
})
