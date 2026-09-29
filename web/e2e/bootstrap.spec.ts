import { expect, test } from '@playwright/test'

const storageSettings = {
  ingest_memory: { global_buffer_bytes: 1073741824, per_recording_buffer_bytes: 805306368, max_payload_bytes: 536870912 },
  queue_writer: { pending_queue_capacity: 128, writer_concurrency: 1 },
  failure_handling: { persist_attempts: 5, retry_initial_backoff_ms: 100, retry_max_backoff_ms: 800 },
  observability: { sampling_interval_ms: 5000, metrics_retention_ms: 86400000 },
}
function settingsResponse() {
  return { settings: { ui: { theme: 'light' as const }, integrity: { concurrency: 1 }, retention: { enabled: false, completed_after_days: 30 }, storage: structuredClone(storageSettings) }, effective_storage: structuredClone(storageSettings), restart_required: [] }
}

test('first-run setup sends bootstrap token and password through authenticated API flow', async ({ page }) => {
  let bootstrapped = false
  let csrfHeader = ''
  const requestedURLs: string[] = []
  page.on('request', request => requestedURLs.push(request.url()))
  await page.route('**/api/auth/session', async route => route.fulfill({ json: {
    auth_enabled: true,
    authenticated: bootstrapped,
    needs_bootstrap: !bootstrapped,
    bootstrap_token_path: '/data/bootstrap-token',
    csrf_token: 'csrf-before-bootstrap',
  } }))
  await page.route('**/api/auth/bootstrap', async route => {
    csrfHeader = route.request().headers()['x-csrf-token'] ?? ''
    const body = route.request().postDataJSON() as { token: string; password: string }
    expect(body).toEqual({ token: 'one-time-token', password: 'a-strong-passphrase' })
    bootstrapped = true
    await route.fulfill({ json: { auth_enabled: true, authenticated: true, needs_bootstrap: false, csrf_token: 'csrf-after-bootstrap' } })
  })
  await page.route('**/api/dashboard', route => route.fulfill({ json: {
    active_recordings_count: 0, completed_last_24h: 0, interrupted_last_24h: 0, recordings_total: 0,
    segments_total: 0, gaps_total: 0, archive_bytes: 0, filesystem_total_bytes: 0,
    filesystem_free_bytes: 0, filesystem_used_bytes: 0, integrity: {}, adapters: {}, export_available: false,
    recent_recordings: [], active_recordings: [],
  } }))
  await page.route('**/api/notifications', route => route.fulfill({ json: { items: [] } }))
  await page.route('**/api/resolve-workflows', route => route.fulfill({ json: [] }))
  await page.route('**/api/adapters', route => route.fulfill({ json: [] }))
  await page.route('**/api/settings', route => route.fulfill({ json: settingsResponse() }))
  await page.route('**/api/system/storage', route => route.fulfill({ json: { filesystem_total_bytes: 0, filesystem_used_bytes: 0, filesystem_available_bytes: 0, recordings_bytes: 0, recording_count: 0, segment_count: 0, init_segment_count: 0, manifest_count: 0 } }))
  await page.goto('/login?mode=bootstrap')
  await expect(page.getByText('/data/bootstrap-token')).toBeVisible()
  await page.getByLabel('초기화 토큰').fill('one-time-token')
  await page.getByLabel('관리자 비밀번호').fill('a-strong-passphrase')
  await page.getByLabel('비밀번호 확인').fill('a-strong-passphrase')
  await page.getByRole('button', { name: '서버 초기화' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('heading', { name: '대시보드' })).toBeVisible()
  expect(csrfHeader).toBe('csrf-before-bootstrap')
  expect(requestedURLs.some(url => url.includes('/data/bootstrap-token'))).toBe(false)
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 768, height: 1024 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport)
    await expect(page.getByRole('heading', { name: '대시보드' })).toBeVisible()
    const width = await page.evaluate(() => ({ body: document.body.scrollWidth, viewport: window.innerWidth }))
    expect(width.body).toBeLessThanOrEqual(width.viewport)
  }
  const menuButton = page.getByRole('button', { name: '메뉴 열기' })
  await expect(menuButton).toHaveAttribute('aria-expanded', 'false')
  await menuButton.click()
  await expect(menuButton).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByRole('navigation', { name: '주 메뉴' })).toBeVisible()
})

test('administrator can log in, use the application, and log out', async ({ page }) => {
  let authenticated = false
  let logoutCSRF = ''
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (!path.startsWith('/api/')) return route.continue()
    if (path === '/api/auth/session') {
      await route.fulfill({ json: { auth_enabled: true, authenticated, needs_bootstrap: false, csrf_token: authenticated ? 'session-csrf' : '' } })
    } else if (path === '/api/auth/login') {
      expect(request.postDataJSON()).toEqual({ password: 'a-strong-passphrase' })
      authenticated = true
      await route.fulfill({ json: { auth_enabled: true, authenticated: true, needs_bootstrap: false, csrf_token: 'session-csrf' } })
    } else if (path === '/api/auth/logout') {
      logoutCSRF = request.headers()['x-csrf-token'] ?? ''
      authenticated = false
      await route.fulfill({ status: 204, body: '' })
    } else if (path === '/api/dashboard') {
      await route.fulfill({ json: { active_recordings_count: 0, completed_last_24h: 0, interrupted_last_24h: 0, recordings_total: 0, segments_total: 0, gaps_total: 0, archive_bytes: 0, filesystem_total_bytes: 0, filesystem_free_bytes: 0, filesystem_used_bytes: 0, integrity: {}, adapters: {}, export_available: false, recent_recordings: [], active_recordings: [] } })
    } else if (path === '/api/system/storage') {
      await route.fulfill({ json: { filesystem_total_bytes: 0, filesystem_used_bytes: 0, filesystem_available_bytes: 0, recordings_bytes: 0, recording_count: 0, segment_count: 0, init_segment_count: 0, manifest_count: 0 } })
    } else if (path === '/api/adapters') {
      await route.fulfill({ json: [] })
    } else if (path === '/api/resolve-workflows') {
      await route.fulfill({ json: [] })
    } else if (path === '/api/notifications') {
      await route.fulfill({ json: { items: [] } })
    } else if (path === '/api/settings') {
      await route.fulfill({ json: settingsResponse() })
    } else {
      await route.fulfill({ status: 404, json: { error: 'unexpected test request' } })
    }
  })

  await page.goto('/login')
  await page.getByLabel('관리자 비밀번호').fill('a-strong-passphrase')
  await page.getByRole('button', { name: '로그인' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('heading', { name: '대시보드' })).toBeVisible()
  await page.getByRole('button', { name: /관리자/ }).click()
  await page.getByRole('button', { name: '로그아웃' }).click()
  await expect(page).toHaveURL('/login')
  expect(logoutCSRF).toBe('session-csrf')
})
