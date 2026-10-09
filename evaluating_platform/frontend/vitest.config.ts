import { defineConfig } from 'vitest/config'

// Vitest for the plain-logic frontend unit tests (see src/**/*.test.ts). These
// tests import only pure modules (no JSX/DOM), so the node environment is
// enough and no React plugin is required.
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
})
