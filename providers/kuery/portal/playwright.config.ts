import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './browser',
  fullyParallel: false,
  reporter: 'line',
  use: {
    headless: true,
  },
})
