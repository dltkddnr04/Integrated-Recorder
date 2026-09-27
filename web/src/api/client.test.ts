import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, setCSRFToken } from './client'
import { recordingsAPI } from './index'

afterEach(() => { vi.unstubAllGlobals(); setCSRFToken(undefined) })

describe('typed API client', () => {
  it('attaches the current CSRF token and same-origin credentials to mutations', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    setCSRFToken('csrf-test')
    await api('/api/example', { method: 'POST', body: { value: 1 } })
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.credentials).toBe('same-origin')
    expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('csrf-test')
    expect(init.body).toBe('{"value":1}')
  })

  it('keeps recording search filters and cursors server-side in the v2 request', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [], total: 0 }), { status: 200, headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    await recordingsAPI.list({ q: 'live show', state: 'completed', tag: 'important', sort: '-started_at', limit: 25, cursor: 'next-page' })
    expect(fetchMock.mock.calls[0]?.[0]).toContain('/api/v2/recordings?q=live+show&state=completed&tag=important&sort=-started_at&limit=25&cursor=next-page')
  })

  it('returns structured API errors with request identifiers', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'conflict' }), { status: 409, headers: { 'content-type': 'application/json', 'X-Request-ID': 'req-1' } })))
    await expect(api('/api/example')).rejects.toMatchObject({ status: 409, message: 'conflict', requestID: 'req-1' })
  })
})
