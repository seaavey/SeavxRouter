import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig, loadEnv } from 'vite'

export default defineConfig(({ mode }) => {
  const target = loadEnv(mode, process.cwd(), 'SEAVX_').SEAVX_ROUTER_ORIGIN || 'http://127.0.0.1:20128'
  const proxy = { '/dashboard/api': { target }, '/healthz': { target } }

  return {
    plugins: [react(), tailwindcss()],
    server: { host: '127.0.0.1', proxy },
    preview: { host: '127.0.0.1', proxy },
  }
})
