import { defineConfig } from 'vite'

// allowedHosts: previews are served under apptown preview addresses the app
// cannot predict; AppTown's proxy is the front door, not Vite.
export default defineConfig({
  build: { outDir: 'dist' },
  preview: { allowedHosts: true },
})
