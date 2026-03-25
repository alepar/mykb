import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/mykb.v1.KBService': {
        target: 'http://localhost:9091',
      },
      '/api': {
        target: 'http://localhost:9091',
      },
    },
  },
})
