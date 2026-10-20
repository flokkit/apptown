import { defineConfig } from 'vite'

// base './' so the built game works from any path it is served under.
// allowedHosts: previews are served under forge preview domains the
// game can't predict; AppTown's proxy is the front door, not vite.
export default defineConfig({
  base: './',
  build: { outDir: 'dist' },
  preview: { allowedHosts: true },
})
