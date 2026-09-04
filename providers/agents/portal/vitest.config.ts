import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// Component tests run against jsdom: the custom elements register for real and
// we drive them through the DOM, which is the only way to test the light-DOM
// rendering + streaming behaviour this portal depends on.
export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    setupFiles: ['src/test/setup.ts'],
  },
})
