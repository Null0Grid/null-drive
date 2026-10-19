import { defineConfig, devices } from '@playwright/test';

// Each `playwright test` run starts the real server binary (with the UI
// embedded) on a brand-new data dir, so the setup wizard is always shown
// first. Build it with `make build` before running.
const port = Number(process.env.E2E_PORT ?? 18181);
const bin = process.env.NULLDRIVE_BIN ?? '../bin/nulldrive';

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'e2e', testMatch: /flow\.spec\.ts/, use: { ...devices['Desktop Chrome'] } },
    { name: 'screenshots', testMatch: /screenshots\.spec\.ts/, use: { ...devices['Desktop Chrome'] } },
    // Records the getting-started video (scripts/tutorial-video.sh turns it into an MP4).
    { name: 'tutorial', testMatch: /tutorial\.spec\.ts/, use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    command: `rm -rf .e2e-data && NULLDRIVE_DATA_DIR=.e2e-data NULLDRIVE_PORT=${port} NULLDRIVE_LOG_LEVEL=warn ${bin}`,
    url: `http://127.0.0.1:${port}/api/v1/health`,
    reuseExistingServer: false,
    timeout: 20_000,
  },
});
