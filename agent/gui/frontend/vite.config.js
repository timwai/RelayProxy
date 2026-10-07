import { writeFileSync } from 'node:fs';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const sentinel = `This directory is populated by agent/gui/frontend (Vite) before Windows release builds.
It intentionally contains no generated bundle in git. When index.html is absent, the Go
desktop shell falls back to the legacy embedded page so ordinary go test/go build remains usable.
`;

export default defineConfig({
  plugins: [
    react(),
    {
      name: 'relayproxy-react-dist-sentinel',
      closeBundle() {
        writeFileSync(new URL('../react_dist/README.txt', import.meta.url), sentinel);
      },
    },
  ],
  base: '/',
  build: {
    outDir: '../react_dist',
    emptyOutDir: true,
    sourcemap: false,
    assetsDir: 'assets'
  }
});
