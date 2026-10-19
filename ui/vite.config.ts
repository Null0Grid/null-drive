/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';
import { fileURLToPath } from 'node:url';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

// The production bundle is written straight into the Go package that embeds
// it (server/internal/webui/dist), so `go build` picks it up with no copy step.
const outDir = fileURLToPath(new URL('../server/internal/webui/dist', import.meta.url));
const api = process.env.NULLDRIVE_API ?? 'http://127.0.0.1:8080';

export default defineConfig({
  // svelteTesting(): browser build of Svelte + auto-cleanup in Vitest.
  plugins: [
    svelte(),
    svelteTesting(),
    {
      // emptyOutDir wipes dist/, including the committed .gitkeep that lets
      // `go build` work before the UI was ever built. Put it back.
      name: 'keep-gitkeep',
      apply: 'build',
      closeBundle: () => writeFileSync(join(outDir, '.gitkeep'), ''),
    },
  ],
  resolve: {
    alias: {
      '$lib': fileURLToPath(new URL('./src/lib', import.meta.url)),
    },
  },
  build: {
    outDir,
    emptyOutDir: true, // outDir is outside the Vite root, so this must be explicit
    target: 'es2022',
    // Never inline assets as data: URIs. The server's CSP only allows
    // fonts/images from 'self', and separate files cache better anyway.
    assetsInlineLimit: 0,
    sourcemap: false,
    // libsodium (WebAssembly inside JS, ~530 KB) is one lazy chunk loaded
    // only by end-to-end encrypted accounts; it can't be split further.
    chunkSizeWarningLimit: 600,
  },
  server: {
    port: 5173,
    // `make dev`: Vite serves the UI with hot reload and forwards API calls
    // to the Go server, so the browser sees one origin (cookies just work).
    proxy: { '/api': { target: api, changeOrigin: false } },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    setupFiles: ['./src/test-setup.ts'],
  },
});
