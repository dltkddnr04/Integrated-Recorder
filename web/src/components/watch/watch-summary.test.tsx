import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router'
import { WatchSummary } from './watch-summary'

describe('dashboard Watch summary', () => {
  it('renders the actual API summary counts', async () => {
    vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
    const root = createRootRoute()
    const summaryRoute = createRoute({ getParentRoute: () => root, path: '/', component: () => <WatchSummary summary={{ total: 5, enabled: 4, recording: 2, offline: 1, backoff: 1, attention_required: 0 }} /> })
    const watchesRoute = createRoute({ getParentRoute: () => root, path: '/watches', component: () => null })
    const router = createRouter({ routeTree: root.addChildren([summaryRoute, watchesRoute]), history: createMemoryHistory({ initialEntries: ['/'] }), scrollRestoration: false })
    await router.load()
    render(<RouterProvider router={router} />)
    expect(screen.getByText('등록된 Watch').parentElement).toHaveTextContent('5')
    expect(screen.getByText('활성 Watch').parentElement).toHaveTextContent('4')
    expect(screen.getByText('녹화 중').parentElement).toHaveTextContent('2')
    expect(screen.getByText('오프라인').parentElement).toHaveTextContent('1')
    expect(screen.getByRole('link', { name: /자동 녹화 관리/ })).toHaveAttribute('href', '/watches')
  })
})
