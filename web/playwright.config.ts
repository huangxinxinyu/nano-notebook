import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  timeout: 30_000,
  expect: { timeout: 8_000 },
  use: {
    baseURL: process.env.NANO_WEB_URL ?? "http://127.0.0.1:5173",
    trace: "retain-on-failure"
  },
  projects: [
    { name: "chromium-desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } },
    { name: "chromium-compact", use: { ...devices["Pixel 5"], viewport: { width: 390, height: 844 } } }
  ]
});

