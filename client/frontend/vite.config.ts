/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';
import { fileURLToPath } from 'node:url';
import { readFileSync, writeFileSync } from 'node:fs';

const lib = fileURLToPath(new URL('../../ui/src/lib', import.meta.url));

export default defineConfig({
  plugins: [
    svelte(),
    svelteTesting(),
    {
      // Keep the committed placeholder so `go build` (go:embed) works
      // before the frontend has ever been built.
      name: 'keep-gitkeep',
      apply: 'build',
      closeBundle: () => writeFileSync(fileURLToPath(new URL('./dist/.gitkeep', import.meta.url)), ''),
    },
  ],
  resolve: {
    alias: { $lib: lib },
    // The shared components live in ../../ui and would otherwise resolve
    // packages from ui/node_modules: two Svelte runtimes break reactivity,
    // and in CI (and a fresh clone) ui/node_modules may not exist at all.
    // Resolve them from this app's own node_modules instead.
    dedupe: ['svelte', '@fontsource/silkscreen', '@fontsource/vt323', '@fontsource/jetbrains-mono'],
  },
  // The demo backend (bridge.ts, browser preview only) shows this version
  // for the servers it pretends to find.
  define: { __DEMO_VERSION__: JSON.stringify('v' + JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8')).version) },
  server: {
    fs: { allow: ['..', '../../ui'] },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    setupFiles: ['./src/test-setup.ts'],
  },
});
