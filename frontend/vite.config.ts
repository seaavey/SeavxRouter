import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'

export default defineConfig(({ mode }) => {
  // A shell export wins over .env.local, so `SEAVX_ROUTER_ORIGIN=… npm run dev`
  // points the proxy somewhere else without editing a file.
  const fromFile = loadEnv(mode, process.cwd(), 'SEAVX_').SEAVX_ROUTER_ORIGIN
  const target = process.env.SEAVX_ROUTER_ORIGIN || fromFile || 'http://127.0.0.1:20128'
  const proxy = { '/dashboard/api': { target }, '/healthz': { target } }

  return {
    plugins: [
      tanstackRouter({ target: 'react', routesDirectory: 'src/routes', generatedRouteTree: 'src/routeTree.gen.ts' }),
      react(),
      tailwindcss(),
    ],
    resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
    server: { host: '127.0.0.1', proxy },
    preview: { host: '127.0.0.1', proxy },
    build: { chunkSizeWarningLimit: 900 },
  }
})
