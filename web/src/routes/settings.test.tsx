import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { dashboardAPI, productAPI } from '@/api'
import { ToastProvider } from '@/components/ui/toast'
import { APIError } from '@/api/client'
import type { StorageSettings, SystemSettings } from '@/types/api'
import { SettingsPage } from './settings'

const storageDefaults: StorageSettings = {
  ingest_memory: { global_buffer_bytes: 1024 ** 3, per_recording_buffer_bytes: 768 * 1024 ** 2, max_payload_bytes: 512 * 1024 ** 2 },
  queue_writer: { pending_queue_capacity: 128, writer_concurrency: 1 },
  failure_handling: { persist_attempts: 5, retry_initial_backoff_ms: 100, retry_max_backoff_ms: 800 },
  observability: { sampling_interval_ms: 5000, metrics_retention_ms: 24 * 60 * 60 * 1000 },
}
const settingsDefaults: SystemSettings = {
  settings: { ui: { theme: 'light' }, integrity: { concurrency: 1 }, retention: { enabled: false, completed_after_days: 30 }, storage: storageDefaults },
  effective_storage: storageDefaults,
  restart_required: [],
}
const storageInfo = { archive_root: '', filesystem_total_bytes: 0, filesystem_used_bytes: 0, filesystem_available_bytes: 0, recordings_bytes: 0, recording_count: 0, segment_count: 0, init_segment_count: 0, manifest_count: 0 }
const systemInfo = { version: 'test', commit: 'test', go_version: 'go', goos: 'linux', goarch: 'amd64', started_at: '2026-09-29T00:00:00Z', uptime_seconds: 1, export_available: false }

function renderSettings() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ToastProvider><SettingsPage /></ToastProvider></QueryClientProvider>)
}
function openStorageTab() { fireEvent.click(screen.getByRole('tab', { name: '저장소' })) }

beforeEach(() => {
  vi.spyOn(productAPI, 'settings').mockResolvedValue(structuredClone(settingsDefaults))
  vi.spyOn(productAPI, 'saveSettings').mockResolvedValue(structuredClone(settingsDefaults))
  vi.spyOn(productAPI, 'retentionCandidates').mockResolvedValue({ enabled: false, candidate_count: 0, candidates: [] })
  vi.spyOn(dashboardAPI, 'storage').mockResolvedValue(storageInfo)
  vi.spyOn(dashboardAPI, 'info').mockResolvedValue(systemInfo)
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('storage ingest settings UI', () => {
  it('loads MiB/seconds values, shows effective values separately, and sends bytes/milliseconds to the API', async () => {
    const saved = structuredClone(settingsDefaults)
    saved.settings.storage.observability.sampling_interval_ms = 10_000
    saved.effective_storage.observability.sampling_interval_ms = 5_000
    saved.restart_required = ['storage.observability.sampling_interval_ms']
    vi.mocked(productAPI.saveSettings).mockResolvedValue(saved)
    renderSettings()
    openStorageTab()

    expect(await screen.findByRole('heading', { name: '고급 수집·저장 설정' })).toBeInTheDocument()
    expect(screen.getByLabelText('전체 버퍼 한도')).toHaveValue(1024)
    expect(screen.getByLabelText('녹화별 버퍼 한도')).toHaveValue(768)
    expect(screen.getByLabelText('단일 페이로드 최대 크기')).toHaveValue(512)
    expect(screen.getByLabelText('측정 간격')).toHaveValue(5)
    expect(screen.getByLabelText('측정 기록 보존 기간')).toHaveValue(24)
    expect(screen.getByLabelText('총 저장 시도 횟수')).toHaveValue(5)
    expect(screen.getByText('최초 저장 시도를 포함한 최대 시도 횟수입니다.')).toBeInTheDocument()
    expect(screen.getByLabelText('저장 작업 동시 실행 수')).toBeDisabled()
    expect(screen.getByText(/높은 동시성이 항상 빠른 것은 아니며/)).toBeInTheDocument()
    expect(within(screen.getByTestId('effective-storage-settings')).getByText('5초')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('측정 간격'), { target: { value: '10' } })
    fireEvent.click(screen.getByRole('button', { name: '수집·저장 설정 저장' }))
    await waitFor(() => expect(productAPI.saveSettings).toHaveBeenCalledWith({ storage: {
      ...storageDefaults,
      observability: { sampling_interval_ms: 10_000, metrics_retention_ms: 86_400_000 },
    } }))
    expect(await screen.findByText(/서버를 재시작해야 적용됩니다/)).toBeInTheDocument()
    expect(within(screen.getByTestId('effective-storage-settings')).getByText('5초')).toBeInTheDocument()
    expect(screen.getByLabelText('측정 간격')).toHaveValue(10)
  })

  it('shows an authoritative server validation error for invalid related limits', async () => {
    vi.mocked(productAPI.saveSettings).mockRejectedValue(new APIError(400, '녹화별 버퍼 한도는 전체 버퍼 한도보다 클 수 없습니다.'))
    renderSettings()
    openStorageTab()
    await screen.findByRole('heading', { name: '고급 수집·저장 설정' })
    fireEvent.change(screen.getByLabelText('녹화별 버퍼 한도'), { target: { value: '1100' } })
    fireEvent.click(screen.getByRole('button', { name: '수집·저장 설정 저장' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('녹화별 버퍼 한도는 전체 버퍼 한도보다 클 수 없습니다.')
  })
})
