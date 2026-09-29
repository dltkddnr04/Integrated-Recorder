import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { StorageMetricSample, StoragePool } from '@/types/api'
import { MetricChart } from '@/components/storage/metric-chart'
import { StoragePoolCard, StoragePoolList } from './storage'

const pool: StoragePool = {
  id: 'local-primary', display_name: '기본 보관 저장소', kind: 'local', role: 'primary', health: 'healthy',
  capacity: { total_bytes: 10 * 1024 ** 4, used_bytes: 7.2 * 1024 ** 4, available_bytes: 2.8 * 1024 ** 4, usage_ratio: 0.72 },
  throughput: { read_bytes_per_second: 12 * 1024 ** 2, write_bytes_per_second: 84 * 1024 ** 2, read_bytes_total: 12_000_000, write_bytes_total: 84_000_000, read_latency_ms: 1.2, write_latency_ms: 9.4 },
  estimated_ceiling: { source: 'unknown' },
  buffer: { used_bytes: 320 * 1024 ** 2, capacity_bytes: 1024 ** 3, utilization: 0.3125 },
  queue: { objects: 42, bytes: 610 * 1024 ** 2, oldest_age_seconds: 5.8 },
  writers: { active: 1, limit: 1 }, errors_total: 0,
}
const samples: StorageMetricSample[] = [
  { at: '2026-09-29T00:00:00Z', read_bytes_per_second: 1024, write_bytes_per_second: 2048, buffer_used_bytes: 4096, persist_queue_bytes: 2048 },
  { at: '2026-09-29T00:00:05Z', read_bytes_per_second: 2048, write_bytes_per_second: 4096, buffer_used_bytes: 2048, persist_queue_bytes: 1024 },
]

describe('storage observability UI', () => {
  it('shows a useful empty state when the pool list is empty', () => {
    render(<StoragePoolList pools={[]} />)
    expect(screen.getByText('등록된 저장 풀이 없습니다.')).toBeInTheDocument()
  })

  it('shows an empty state when the pool has no metric history yet', () => {
    render(<MetricChart title="Recorder 읽기/쓰기" samples={[]} kind="throughput" pool={pool} />)
    expect(screen.getByText('표시할 측정 기록이 없습니다.')).toBeInTheDocument()
  })

  it('shows the API pool identity, capacity, recorder rates, buffer, queue, writer and unknown ceiling', async () => {
    vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
    const root = createRootRoute()
    const cardRoute = createRoute({ getParentRoute: () => root, path: '/', component: () => <StoragePoolCard pool={pool} /> })
    const detailRoute = createRoute({ getParentRoute: () => root, path: '/storage/$poolId', component: () => null })
    const router = createRouter({ routeTree: root.addChildren([cardRoute, detailRoute]), history: createMemoryHistory({ initialEntries: ['/'] }), scrollRestoration: false })
    await router.load()
    render(<RouterProvider router={router} />)
    expect(screen.getByText(/local-primary/)).toBeInTheDocument()
    expect(screen.getByText('Recorder 읽기')).toBeInTheDocument()
    expect(screen.getByText('Recorder 쓰기')).toBeInTheDocument()
    expect(screen.getByText('7.20 TB / 10.0 TB')).toBeInTheDocument()
    expect(screen.getByText('수집 버퍼')).toBeInTheDocument()
    expect(screen.getByText('320 MB / 1.00 GB')).toBeInTheDocument()
    expect(screen.getByText('저장 대기열')).toBeInTheDocument()
    expect(screen.getByText('42개 · 610 MB')).toBeInTheDocument()
    expect(screen.getByText('예상 상한: 알 수 없음')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /상세 보기/ })).toHaveAttribute('href', '/storage/local-primary')
  })

  it('renders the sample trend as an accessible SVG without inventing an unknown ceiling reference', () => {
    const { container } = render(<MetricChart title="Recorder 읽기/쓰기" samples={samples} kind="throughput" pool={pool} />)
    expect(screen.getByRole('img', { name: /Recorder 읽기\/쓰기/ })).toBeInTheDocument()
    expect(screen.getByText('Recorder 읽기')).toBeInTheDocument()
    expect(screen.getByText('Recorder 쓰기')).toBeInTheDocument()
    expect(container.querySelector('svg polyline')).toBeInTheDocument()
    expect(container.querySelector('line[stroke-dasharray="7 5"]')).not.toBeInTheDocument()
    expect(container.querySelectorAll('circle')).toHaveLength(4)
  })
})
