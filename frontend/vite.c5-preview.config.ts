import { mergeConfig } from 'vite'
import baseConfig from './vite.config'

export default mergeConfig(baseConfig, {
  cacheDir: '../.cache/c5-preview/vite',
  server: {
    host: '100.107.57.101',
    port: 5445,
    strictPort: true,
    proxy: {
      '/api': { target: 'http://127.0.0.1:5556', changeOrigin: true },
    },
  },
})
