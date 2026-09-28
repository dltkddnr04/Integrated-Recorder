import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { WatchPreview } from './watch-preview'
import type { WatchView } from '@/types/api'

function watch(ordinal: number): WatchView {
  return {
    id: 'watch-1', adapter_id: 'owncast', input: {}, enabled: true, preview_mode: 'segment',
    check_interval_seconds: 10, state: 'recording', created_at: '2026-09-28T00:00:00Z', updated_at: '2026-09-28T00:00:00Z',
    input_secret_configured: {}, current_recording_id: 'recording-1',
    current_recording_preview: { mode: 'segment', state: 'partial', available: true, frame_count: ordinal, image_archive_ordinal: ordinal },
  }
}

describe('WatchPreview', () => {
  it('keeps the live image visible until the next frame loads, then cross-fades', async () => {
    const view = render(<WatchPreview watch={watch(1)} />)
    const first = await waitFor(() => {
      const image = view.container.querySelector<HTMLImageElement>('[data-preview-pending="true"]')
      expect(image).not.toBeNull()
      return image!
    })
    fireEvent.load(first)
    await waitFor(() => expect(view.container.querySelector('img:not([data-preview-pending])')).toHaveAttribute('src', '/api/recordings/recording-1/previews/1'))

    view.rerender(<WatchPreview watch={watch(2)} />)
    const next = await waitFor(() => {
      const image = view.container.querySelector<HTMLImageElement>('[data-preview-pending="true"]')
      expect(image?.getAttribute('src')).toContain('/previews/2')
      return image!
    })
    expect(view.container.querySelector('img:not([data-preview-pending])')).toHaveAttribute('src', '/api/recordings/recording-1/previews/1')
    fireEvent.load(next)
    await waitFor(() => expect(view.container.querySelector('.live-preview-fade-in')).toHaveAttribute('src', '/api/recordings/recording-1/previews/2'))
    expect(view.container.querySelector('.live-preview-fade-out')).toHaveAttribute('src', '/api/recordings/recording-1/previews/1')
  })

  it('falls back safely when a preview frame cannot be loaded', async () => {
    const view = render(<WatchPreview watch={watch(1)} />)
    const image = await waitFor(() => view.container.querySelector<HTMLImageElement>('[data-preview-pending="true"]')!)
    fireEvent.error(image)
    expect(await screen.findByLabelText('현재 방송 미리보기 대기 화면')).toBeInTheDocument()
    expect(view.container.querySelector('.live-preview-fade-in')).toBeNull()
  })
})
