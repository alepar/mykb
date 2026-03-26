import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
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
