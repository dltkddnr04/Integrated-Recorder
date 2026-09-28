import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { PreviewFrameGrid, PreviewThumbnail } from './previews'
import { formatPreviewClock, seekToPreview, uniquePreviewFrames } from '@/lib/previews'
import type { PreviewFrame, PreviewSummary } from '@/types/api'

const frame = (ordinal: number, time: number, state?: PreviewFrame['state']): PreviewFrame => ({
  archive_ordinal: ordinal, track_id: 'main', source_epoch: 0, sequence: ordinal,
  segment_start_seconds: time - 3, segment_duration_seconds: 6, frame_time_seconds: time,
  segment_sha256: 'sha256', width: 480, height: 270, size: 100, generated_at: '2026-09-28T00:00:00Z', state,
})

describe('Preview Frame Index presentation', () => {
  it('deduplicates ordinals without repeating frames and orders them by playback time', () => {
    expect(uniquePreviewFrames([frame(3, 12), frame(1, 3), frame(1, 4), frame(2, 6, 'failed')]).map(item => item.archive_ordinal)).toEqual([1, 3])
    const view = render(<PreviewFrameGrid recordingId="rec-1" items={[frame(3, 12), frame(1, 3), frame(1, 4)]} onSeek={() => undefined} />)
    expect(screen.getAllByRole('button')).toHaveLength(2)
    expect(screen.getByRole('button', { name: '0m 03s 위치로 이동' })).toBeInTheDocument()
    expect(view.container.querySelector('[aria-label="장면 미리보기"]')).toHaveClass('grid-cols-3', 'md:grid-cols-6', 'xl:grid-cols-8')
  })

  it('seeks to the selected frame when its storyboard button is activated', () => {
    const onSeek = vi.fn()
    render(<PreviewFrameGrid recordingId="rec-1" items={[frame(7, 30)]} onSeek={onSeek} />)
    fireEvent.click(screen.getByRole('button', { name: '0m 30s 위치로 이동' }))
    expect(onSeek).toHaveBeenCalledWith(30)
  })

  it('seeks by assigning currentTime only, preserving native paused/playing state', () => {
    const video = { currentTime: 0, paused: false, play: vi.fn(), pause: vi.fn() }
    expect(seekToPreview(video, 42.5)).toBe(true)
    expect(video.currentTime).toBe(42.5)
    expect(video.paused).toBe(false)
    expect(video.play).not.toHaveBeenCalled()
    expect(video.pause).not.toHaveBeenCalled()
  })

  it('uses a frame while available and falls back when the image errors', () => {
    const summary: PreviewSummary = { mode: 'segment', state: 'ready', available: true, frame_count: 2, image_archive_ordinal: 2, updated_at: 'v1' }
    const view = render(<PreviewThumbnail recordingId="rec-1" summary={summary} adapterId="owncast" adapterName="Owncast" />)
    const image = view.container.querySelector('img')
    expect(image).toHaveAttribute('src', '/api/recordings/rec-1/previews/2?v=v1')
    fireEvent.error(image!)
    expect(view.container.querySelector('img')).toBeNull()
    view.rerender(<PreviewThumbnail recordingId="rec-1" summary={{ ...summary, image_archive_ordinal: 3, updated_at: 'v2' }} adapterId="owncast" adapterName="Owncast" />)
    expect(view.container.querySelector('img')).toHaveAttribute('src', '/api/recordings/rec-1/previews/3?v=v2')
  })

  it('keeps overlays compact and safe for invalid timestamps', () => {
    expect(formatPreviewClock(5076)).toBe('01:24:36')
    expect(formatPreviewClock(Number.NaN)).toBe('—')
  })
})
