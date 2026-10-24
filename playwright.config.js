const { defineConfig } = require('@playwright/test');

const port = process.env.PORT || '4173';

module.exports = defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  use: { baseURL: `http://127.0.0.1:${port}`, browserName: 'chromium' },
  webServer: {
    command: `ADDR=127.0.0.1:${port} go run .`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
