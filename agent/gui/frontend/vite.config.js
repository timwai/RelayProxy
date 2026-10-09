import { writeFileSync } from 'node:fs';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const sentinel = `This directory is populated by agent/gui/frontend (Vite) before Agent release builds
on Linux, macOS and Windows. The React bundle is shared by the Agent Web browser,
macOS WKWebView and Windows Wails shell. Generated assets are intentionally
not checked in; Go-only development builds retain the legacy fallback.
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
