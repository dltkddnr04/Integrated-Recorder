import { expect, test, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const dataDir = process.env.IR_E2E_DATA_DIR
if (!dataDir) throw new Error('IR_E2E_DATA_DIR was not provided by Playwright config')
const expectedSegment = 'integrated-recorder-browser-e2e-source-segment'

type RecordingWire = {
  state: string
  tracks?: Record<string, { segments?: unknown[] }>
}
type SessionWire = { needs_bootstrap: boolean }
type IntegrityWire = { status: string; objects_total: number; objects_corrupt: number }
type TagsWire = { tags: string[] }
type ArchiveIndexWire = { entries: { path: string }[] }
type RecordingPageWire = { items: RecordingListWire[] }
type RecordingListWire = { id: string; adapter_id: string; adapter_name: string; state: string; tags: string[] }

test.describe.configure({ mode: 'serial' })

test('actual Go backend: bootstrap, Owncast capture, VOD, management, and delete', async ({ page }) => {
  const bootstrapToken = readFileSync(join(dataDir, 'security', 'bootstrap-token'), 'utf8').trim()
  const sourceURL = readFileSync(join(dataDir, 'e2e-source-url'), 'utf8').trim()
  const requests: string[] = []
  const cspConsoleErrors: string[] = []
  const iconStatuses: number[] = []
  await page.addInitScript(() => {
    const target = window as typeof window & { __cspViolations?: string[] }
    target.__cspViolations = []
    document.addEventListener('securitypolicyviolation', event => {
      target.__cspViolations?.push(`${event.effectiveDirective}: ${event.blockedURI}`)
    })
  })
  page.on('console', message => {
    if (message.type() === 'error' && message.text().includes('Content Security Policy')) cspConsoleErrors.push(message.text())
  })
  page.on('request', request => requests.push(new URL(request.url()).pathname))
  page.on('response', response => {
    if (new URL(response.url()).pathname === '/api/adapters/owncast/icon') iconStatuses.push(response.status())
  })
  await page.goto('/login?mode=bootstrap')
  await assertResponsive(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await expect(page.getByLabel('초기화 토큰')).toBeVisible()
  await page.getByLabel('초기화 토큰').fill(bootstrapToken)
  await page.getByLabel('관리자 비밀번호').fill('browser-e2e-strong-password')
  await page.getByLabel('비밀번호 확인').fill('browser-e2e-strong-password')
  await page.getByRole('button', { name: '서버 초기화' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('heading', { name: '대시보드' })).toBeVisible()
  await expectOwncastLogo(page)
  expect(requests.some(path => path.includes('bootstrap-token'))).toBe(false)

  for (const route of ['/', '/recordings', '/new', '/adapters', '/adapters/owncast', '/workflows', '/settings']) {
    await page.goto(route)
    await assertResponsive(page)
  }
  await page.goto('/recordings')
  await page.getByRole('combobox').first().click()
  await page.keyboard.press('Escape')
  await expect.poll(() => page.evaluate(() => (window as typeof window & { __cspViolations?: string[] }).__cspViolations ?? [])).toEqual([])
  await page.goto('/new')
  await expectOwncastLogo(page)
  await page.getByRole('button', { name: /Owncast/ }).click()
  await page.getByRole('button', { name: /입력 설정/ }).click()
  await page.getByLabel('Owncast 인스턴스 URL').fill(sourceURL)
  await page.getByLabel('녹화 제목').fill('Browser E2E Owncast capture')
  await page.getByRole('button', { name: '입력 확인' }).click()
  await page.getByRole('button', { name: '녹화 시작' }).click()
  await expect(page).toHaveURL(/\/recordings\/[^/]+$/)
  const recordingID = page.url().split('/').at(-1)!

  await expect.poll(async () => segmentCount(await getJSON<RecordingWire>(page, `/api/recordings/${encodeURIComponent(recordingID)}`))).toBeGreaterThan(0)
  await expect(page.getByText('녹화 중에는 VOD를 재생할 수 없습니다.')).toBeVisible()
  await page.getByRole('button', { name: '중지', exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: '중지', exact: true }).click()
  await expect.poll(async () => (await getJSON<RecordingWire>(page, `/api/recordings/${encodeURIComponent(recordingID)}`)).state).toBe('stopped')

  await expect.poll(() => requests.some(path => path === `/api/recordings/${recordingID}/play/master.m3u8`)).toBe(true)
  const vod = await readVOD(page, recordingID)
  expect(vod.masterStatus).toBe(200)
  expect(vod.playlistStatus).toBe(200)
  expect(vod.segmentStatus).toBe(200)
  expect(vod.segmentText).toBe(expectedSegment)

  await page.getByRole('button', { name: '편집' }).click()
  await page.getByLabel('태그 목록').fill('browser-e2e, source-preserved')
  await page.getByRole('button', { name: '저장', exact: true }).click()
  await expect(page.getByText('browser-e2e', { exact: true })).toBeVisible()
  expect((await getJSON<TagsWire>(page, `/api/recordings/${recordingID}/tags`)).tags).toEqual(['browser-e2e', 'source-preserved'])

  await page.getByRole('button', { name: '무결성 검사' }).click()
  await expect.poll(async () => (await getJSON<IntegrityWire>(page, `/api/recordings/${recordingID}/integrity`)).status).toBe('verified')
  const integrity = await getJSON<IntegrityWire>(page, `/api/recordings/${recordingID}/integrity`)
  expect(integrity.objects_total).toBeGreaterThan(0)
  expect(integrity.objects_corrupt).toBe(0)

  await page.getByRole('tab', { name: '보관 데이터 목록' }).click()
  await expect(page.getByText('recording.json', { exact: true })).toBeVisible()
  const archive = await getJSON<ArchiveIndexWire>(page, `/api/recordings/${recordingID}/archive/index`)
  expect(archive.entries.some((entry: { path: string }) => entry.path.endsWith('.ts'))).toBe(true)

  const exportResponse = await page.request.get(`/api/recordings/${recordingID}/exports`)
  expect(exportResponse.status()).toBe(501)
  await expect(page.getByText('이 서버에서 내보내기를 사용할 수 없습니다.')).toBeVisible()

  const pageWire = await getJSON<RecordingPageWire>(page, '/api/v2/recordings?state=stopped&limit=25')
  const listItem = pageWire.items.find(item => item.id === recordingID)
  expect(listItem).toMatchObject({ id: recordingID, adapter_id: 'owncast', adapter_name: 'Owncast', state: 'stopped', tags: ['browser-e2e', 'source-preserved'] })
  expect('adapter' in listItem).toBe(false)
  expect('resource' in listItem).toBe(false)
  await page.goto('/recordings?state=stopped')
  await expectOwncastLogo(page)
  await expect(page.getByRole('link', { name: 'Browser E2E Owncast capture' }).first()).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'Browser E2E Owncast capture' }).getByText('중지됨', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('row').filter({ hasText: 'Browser E2E Owncast capture' }).getByText('Owncast', { exact: true }),
  ).toBeVisible()
  await page.getByRole('link', { name: /Browser E2E Owncast capture/ }).click()
  await expect(page).toHaveURL(new RegExp(`/recordings/${recordingID}$`))
  await expectOwncastLogo(page)

  await assertResponsive(page)

  await page.getByRole('button', { name: '삭제' }).click()
  await expect(page.getByRole('alertdialog')).toContainText('되돌릴 수 없습니다')
  await expect.poll(() => page.evaluate(() => (window as typeof window & { __cspViolations?: string[] }).__cspViolations ?? [])).toEqual([])
  expect(cspConsoleErrors).toEqual([])
  await page.getByRole('alertdialog').getByRole('button', { name: '보관 데이터 삭제' }).click()
  await expect(page).toHaveURL(/\/recordings(?:\?.*)?$/)
  expect(iconStatuses).toContain(200)
  expect(iconStatuses.every(status => status === 200 || status === 304)).toBe(true)
  await expect(page.getByText('Browser E2E Owncast capture')).toHaveCount(0)
  expect((await page.request.get(`/api/recordings/${recordingID}`)).status()).toBe(404)

  for (const path of ['/login', '/', '/recordings', '/recordings/demo', '/new', '/adapters', '/adapters/owncast', '/workflows', '/workflows/demo', '/settings']) {
    const response = await page.request.get(path, { headers: { accept: 'text/html' } })
    expect(response.status(), `${path} should be served as an SPA route`).toBe(200)
    expect(response.headers()['content-type']).toContain('text/html')
  }
  for (const path of ['/foo', '/recordings/a/b', '/api/unknown', '/static/unknown']) {
    expect((await page.request.get(path, { headers: { accept: 'text/html' } })).status(), `${path} must not be consumed by SPA fallback`).toBe(404)
  }
})

async function expectOwncastLogo(page: Page) {
  const image = page.locator('img[src="/api/adapters/owncast/icon"]').first()
  await expect(image).toBeVisible()
  await expect.poll(() => image.evaluate(element => (element as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
}

test('actual workflow adapter: challenge, secret, action URL, continue, cancel, and history', async ({ page }) => {
  const manifestURL = `${readFileSync(join(dataDir, 'e2e-source-url'), 'utf8').trim()}/hls/stream.m3u8`
  const ephemeralSecret = 'ephemeral-browser-secret'
  let continuationBody = ''
  page.on('request', request => {
    if (new URL(request.url()).pathname.endsWith('/continue')) continuationBody = request.postData() ?? ''
  })
  await login(page)
  await page.goto('/new')
  await page.getByRole('button', { name: /Workflow Fixture/ }).click()
  await page.getByRole('button', { name: /입력 설정/ }).click()
  await page.getByLabel('Fixture source URL').fill(manifestURL)
  await page.getByRole('button', { name: '입력 확인' }).click()
  await page.getByRole('button', { name: '녹화 시작' }).click()
  await expect(page).toHaveURL(/\/workflows\/[^/]+$/)
  const firstWorkflowID = page.url().split('/').at(-1)!

  const actionLink = page.getByRole('link', { name: 'Open fixture action' })
  await expect(actionLink).toHaveAttribute('href', 'https://example.test/confirm')
  await expect(actionLink).toHaveAttribute('target', '_blank')
  await expect(actionLink).toHaveAttribute('rel', /noopener/)
  await expect(actionLink).toHaveAttribute('rel', /noreferrer/)
  await assertResponsive(page)
  await page.getByLabel('Fixture answer').fill('continue')
  await page.getByLabel('Fixture secret').fill(ephemeralSecret)
  await page.getByRole('button', { name: '계속 진행' }).click()
  await expect.poll(() => continuationBody).not.toBe('')
  const submitted = JSON.parse(continuationBody) as { secrets?: Record<string, string>; persist_fields?: string[] }
  expect(submitted.secrets?.session_value).toBe(ephemeralSecret)
  expect(submitted.persist_fields ?? []).not.toContain('session_value')
  await expect(page).toHaveURL(/\/recordings\/[^/]+$/)
  const recordingID = page.url().split('/').at(-1)!
  await expect.poll(async () => segmentCount(await getJSON<RecordingWire>(page, `/api/recordings/${recordingID}`))).toBeGreaterThan(0)
  expect(JSON.stringify(await getJSON(page, '/api/workflow-history'))).not.toContain(ephemeralSecret)
  expect(JSON.stringify(await getJSON(page, '/api/logs?limit=100'))).not.toContain(ephemeralSecret)
  await page.getByRole('button', { name: '중지', exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: '중지', exact: true }).click()
  await expect.poll(async () => (await getJSON<RecordingWire>(page, `/api/recordings/${recordingID}`)).state).toBe('stopped')

  await page.goto(`/workflows/${firstWorkflowID}`)
  await expect(page.getByRole('heading', { name: '워크플로를 찾을 수 없습니다' })).toBeVisible()

  await page.goto('/new')
  await page.getByRole('button', { name: /Workflow Fixture/ }).click()
  await page.getByRole('button', { name: /입력 설정/ }).click()
  await page.getByLabel('Fixture source URL').fill(manifestURL)
  await page.getByRole('button', { name: '입력 확인' }).click()
  await page.getByRole('button', { name: '녹화 시작' }).click()
  await expect(page).toHaveURL(/\/workflows\/[^/]+$/)
  await page.getByRole('button', { name: '취소' }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: '워크플로 취소' }).click()
  await expect(page).toHaveURL('/workflows')
  await expect(page.getByText(/Workflow Fixture · 취소됨/)).toBeVisible()
})

async function login(page: Page) {
  await page.goto('/login')
  const session = await getJSON<SessionWire>(page, '/api/auth/session')
  if (session.needs_bootstrap) {
    const bootstrapToken = readFileSync(join(dataDir!, 'security', 'bootstrap-token'), 'utf8').trim()
    await page.goto('/login?mode=bootstrap')
    await page.getByLabel('초기화 토큰').fill(bootstrapToken)
    await page.getByLabel('관리자 비밀번호').fill('browser-e2e-strong-password')
    await page.getByLabel('비밀번호 확인').fill('browser-e2e-strong-password')
    await page.getByRole('button', { name: '서버 초기화' }).click()
  } else {
    await page.getByLabel('관리자 비밀번호').fill('browser-e2e-strong-password')
    await page.getByRole('button', { name: '로그인' }).click()
  }
  await expect(page).toHaveURL('/')
}

async function assertResponsive(page: Page) {
  for (const viewport of [
    { width: 390, height: 844 }, { width: 430, height: 932 }, { width: 768, height: 1024 },
    { width: 1024, height: 768 }, { width: 1440, height: 900 },
  ]) {
    await page.setViewportSize(viewport)
    const width = await page.evaluate(() => ({ body: document.body.scrollWidth, viewport: window.innerWidth }))
    expect(width.body).toBeLessThanOrEqual(width.viewport)
  }
}

async function getJSON<T>(page: Page, path: string): Promise<T> {
  return page.evaluate(async url => {
    const response = await fetch(url)
    if (!response.ok) throw new Error(`GET ${url}: ${response.status}`)
    return await response.json() as T
  }, path)
}

async function segmentCount(recording: RecordingWire): Promise<number> {
  return Object.values(recording.tracks ?? {}).reduce((count, track) => count + (track.segments?.length ?? 0), 0)
}

async function readVOD(page: Page, recordingID: string) {
  return page.evaluate(async id => {
    const base = `/api/recordings/${encodeURIComponent(id)}/play/`
    const master = await fetch(`${base}master.m3u8`)
    const masterText = await master.text()
    const trackPath = masterText.split(/\r?\n/).find(line => line && !line.startsWith('#'))
    if (!trackPath) throw new Error('master playlist had no track path')
    const playlist = await fetch(new URL(trackPath, `${location.origin}${base}`).toString())
    const playlistText = await playlist.text()
    const segmentPath = playlistText.split(/\r?\n/).find(line => line && !line.startsWith('#'))
    if (!segmentPath) throw new Error('track playlist had no media path')
    const segment = await fetch(new URL(segmentPath, `${location.origin}${base}`).toString())
    return {
      masterStatus: master.status,
      playlistStatus: playlist.status,
      segmentStatus: segment.status,
      segmentText: await segment.text(),
    }
  }, recordingID)
}
