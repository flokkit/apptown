import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import precompress from './precompress.js'

export default defineConfig({
  plugins: [react(), precompress()],
  server: {
    proxy: { '/api': 'http://localhost:7777' },
  },
  test: {
    // Pure logic runs in node; a test that needs a DOM says so at its top
    // (// @vitest-environment jsdom).
    environment: 'node',
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
