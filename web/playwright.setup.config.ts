import { defineConfig, devices } from '@playwright/test'

const dataDir = process.env.IR_SETUP_E2E_DATA_DIR
const setupCode = process.env.IR_SETUP_E2E_CODE
const restartFile = process.env.IR_SETUP_E2E_RESTART_FILE
if (!dataDir || !setupCode || !restartFile) {
  throw new Error('Run the real-product first-run E2E through `npm run test:e2e:setup`.')
}

export default defineConfig({
  testDir: './e2e',
  testMatch: 'setup-first-run.spec.ts',
  outputDir: './test-results/setup-first-run',
  fullyParallel: false,
  workers: 1,
  reporter: 'list',
  timeout: 180_000,
  expect: { timeout: 20_000 },
  use: {
    baseURL: 'http://127.0.0.1:4173',
    // The setup-code field is an authentication secret; never persist browser
    // traces that might capture submitted form values.
    trace: 'off',
    ...devices['Desktop Chrome'],
    channel: 'chrome',
  },
})
