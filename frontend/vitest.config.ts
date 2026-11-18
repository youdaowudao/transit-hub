import path from 'node:path'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

const isCI = process.env.CI === 'true'
// Exercise the existing React report components alongside the Vue host.
const chanshuiAliases = Object.fromEntries([
  'lib/i18n', 'lib/i18n-rows', 'lib/report-view', 'lib/model-picker', 'lib/knowledge-dates',
  'components/AnimatedNumber', 'components/ResultSkeleton', 'components/ErrorFold',
  'components/Hint', 'components/Icon', 'components/ProbeEvidence', 'components/ElapsedTime',
].map(name => [`@/${name}`, path.resolve(__dirname, '../chanshui/web', name)]))

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      ...chanshuiAliases,
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
