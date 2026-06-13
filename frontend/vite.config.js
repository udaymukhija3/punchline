import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// In dev, the app talks to itself (same-origin relative URLs) and Vite proxies
// API + WebSocket traffic to the Go backend. In production the Go server serves
// the built assets, so the same relative URLs work without any config.
export default defineConfig({
  plugins: [react()],
  server: {
    host: '0.0.0.0',
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/ws': { target: 'ws://localhost:8080', ws: true },
      '/healthz': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
});
