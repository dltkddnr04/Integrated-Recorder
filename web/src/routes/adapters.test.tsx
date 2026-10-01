import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AdaptersPage } from './adapters'
import { ToastProvider } from '@/components/ui/toast'

afterEach(() => vi.unstubAllGlobals())

function renderAdapters() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  client.setQueryData(['adapters'], [])
  return render(<QueryClientProvider client={client}><ToastProvider><AdaptersPage /></ToastProvider></QueryClientProvider>)
}

describe('adapter discovery controls', () => {
  it('requests an immediate Host reconciliation and shows its safe result', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const body = init?.method === 'POST'
        ? { state: 'activated', active_adapter_count: 2, rejected_count: 0, generation_id: 'gen-123' }
        : []
      return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetchMock)

    renderAdapters()
    fireEvent.click(await screen.findByRole('button', { name: '어댑터 다시 검색' }))

    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => url === '/api/runtime/adapters/reconcile' && init?.method === 'POST')).toBe(true))
    expect(await screen.findByText('새 어댑터 세대를 활성화했습니다.')).toBeInTheDocument()
    expect(screen.getByText(/Runtime Host가 자동으로 검증하고 활성화합니다/)).toBeInTheDocument()
  })
})
