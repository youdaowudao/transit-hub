import path from 'node:path'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

const isCI = process.env.CI === 'true'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
    pool: 'forks',
    ...(isCI ? {} : { maxWorkers: 2, minWorkers: 1 }),
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json-summary'],
    },
  },
})
