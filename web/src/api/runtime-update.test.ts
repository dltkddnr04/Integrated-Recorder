import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCSRFToken } from './client'
import { runtimeUpdateAPI } from './index'

afterEach(() => { vi.unstubAllGlobals(); setCSRFToken(undefined) })

describe('runtime update API', () => {
  it('uses the authenticated status and mutation endpoints', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    setCSRFToken('csrf-test')

    await runtimeUpdateAPI.status()
    await runtimeUpdateAPI.check()
    await runtimeUpdateAPI.stage()
    await runtimeUpdateAPI.activate()
    await runtimeUpdateAPI.rollback()

    expect(fetchMock.mock.calls.map(([path, options]) => [path, options?.method])).toEqual([
      ['/api/runtime/update', 'GET'],
      ['/api/runtime/update/check', 'POST'],
      ['/api/runtime/update/stage', 'POST'],
      ['/api/runtime/update/activate', 'POST'],
      ['/api/runtime/update/rollback', 'POST'],
    ])
    for (const [, options] of fetchMock.mock.calls.slice(1)) {
      const headers = new Headers(options?.headers)
      expect(headers.get('X-CSRF-Token')).toBe('csrf-test')
      expect(options?.body).toBe('{}')
    }
  })
})
