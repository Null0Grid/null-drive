/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';
import { fileURLToPath } from 'node:url';
import { writeFileSync } from 'node:fs';

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
    // `svelte` from ui/node_modules: two Svelte runtimes break reactivity.
    dedupe: ['svelte'],
  },
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
