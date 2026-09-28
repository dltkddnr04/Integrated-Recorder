import { recordingsAPI } from '@/api'
import type { PreviewFrame } from '@/types/api'

export function previewFrameURL(recordingId: string, ordinal: number, version?: string) {
  const base = recordingsAPI.previewFrame(recordingId, ordinal)
  return version ? `${base}?v=${encodeURIComponent(version)}` : base
}

export function uniquePreviewFrames(items: PreviewFrame[]) {
  const byOrdinal = new Map<number, PreviewFrame>()
  for (const item of items) {
    if (!Number.isSafeInteger(item.archive_ordinal) || item.archive_ordinal < 0) continue
    if (item.state && item.state !== 'ready') continue
    if (!byOrdinal.has(item.archive_ordinal)) byOrdinal.set(item.archive_ordinal, item)
  }
  return [...byOrdinal.values()].sort((a, b) => a.frame_time_seconds - b.frame_time_seconds || a.archive_ordinal - b.archive_ordinal)
}

export function formatPreviewClock(seconds: number) {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  const whole = Math.floor(seconds)
  const hours = Math.floor(whole / 3600)
  const minutes = Math.floor((whole % 3600) / 60)
  const remaining = whole % 60
  return `${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}:${String(remaining).padStart(2, '0')}`
}

export function seekToPreview(video: Pick<HTMLVideoElement, 'currentTime'> | null, seconds: number) {
  if (!video || !Number.isFinite(seconds) || seconds < 0) return false
  video.currentTime = seconds
  return true
}
