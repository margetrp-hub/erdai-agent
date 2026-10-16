import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Preserve the browser host for the server's same-origin write checks in local development.
const apiProxy = { target: 'http://127.0.0.1:6282', xfwd: true };

export default defineConfig({
  plugins: [react()],
  base: '/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': apiProxy,
      '/auth': apiProxy,
      '/healthz': apiProxy,
      '/icons': apiProxy,
    },
  },
});
