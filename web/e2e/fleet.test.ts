import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { startDashboard, type Dashboard } from './harness'

let app: Dashboard
beforeAll(async () => { app = await startDashboard(20493) }, 30000)
afterAll(async () => { if (app) await app.stop() })

describe('fleet dashboard', () => {
  test('explains follower ownership and loads from the gateway origin', async () => {
    // Copy only the throwaway login cookie into a fresh context. Its HTTP
    // cache is empty, so the timing below measures an actual first load.
    const browser = app.page.context().browser()!
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    await context.addCookies(await app.page.context().cookies())
    const page = await context.newPage()
    try {
      await page.route('**/api/fleet/status', route => route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          role: 'follower',
          runningCommit: 'a'.repeat(40),
          authorityUrl: 'https://authority.example.test:20130',
          probation: false,
          draining: false,
          sync: { revision: 7, hash: 'b'.repeat(64), lastSuccess: new Date().toISOString() },
        }),
      }))
      await page.goto(app.baseURL + '/dashboard/endpoint')
      await page.getByText('Fleet follower', { exact: true }).waitFor()
      expect(await page.getByRole('link', { name: 'Edit shared configuration on authority' }).getAttribute('href'))
        .toBe('https://authority.example.test:20130')
      expect(await page.getByText('Shared routing settings are read only here. Credentials and device settings stay local.').isVisible())
        .toBe(true)
      const metrics = await page.evaluate(() => {
        const navigation = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming
        const resources = performance.getEntriesByType('resource') as PerformanceResourceTiming[]
        return {
          bytesTransferred: navigation.transferSize + resources.reduce((sum, item) => sum + item.transferSize, 0),
          requestCount: resources.length + 1,
          loadEventMs: Math.round(navigation.loadEventEnd),
          foreignOrigins: resources.map(item => item.name).filter(name => new URL(name).origin !== location.origin),
        }
      })
      expect(metrics.foreignOrigins).toEqual([])
      expect(metrics.bytesTransferred).toBeGreaterThan(0)
      expect(metrics.loadEventMs).toBeGreaterThan(0)
      console.log('Fleet dashboard first load:', JSON.stringify(metrics))
    } finally {
      await context.close()
    }
  }, 20000)
})
