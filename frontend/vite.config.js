import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Dev-server proxy so `npm run dev` works without Docker.
// In production, nginx handles all of this.
export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
      '/auth': 'http://localhost:8080',
      '/dev':  'http://localhost:8080',
    },
  },
})
