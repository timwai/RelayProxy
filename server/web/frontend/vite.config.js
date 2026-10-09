import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  base: '/server-assets/',
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:21080',
      '/ui': 'http://127.0.0.1:21080'
    }
  },
  build: {
    outDir: '../react_dist',
    assetsDir: 'assets',
    emptyOutDir: true,
    sourcemap: false
  }
});