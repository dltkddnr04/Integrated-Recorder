import { render, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RecordingPlayer } from './player'

const hlsState = vi.hoisted(() => ({ destroy: vi.fn(), loadSource: vi.fn(), attachMedia: vi.fn(), on: vi.fn() }))
vi.mock('hls.js', () => ({ default: class HlsMock {
  static Events = { ERROR: 'error' }
  static isSupported() { return true }
  constructor() {}
  loadSource(source: string) { hlsState.loadSource(source) }
  attachMedia(video: HTMLVideoElement) { hlsState.attachMedia(video) }
  on(event: string, callback: (...args: unknown[]) => void) { hlsState.on(event, callback) }
  destroy() { hlsState.destroy() }
} }))

afterEach(() => vi.clearAllMocks())

describe('RecordingPlayer lifecycle', () => {
  it('destroys hls.js and clears the media element when unmounted', async () => {
    const view = render(<RecordingPlayer recordingId="rec-1" />)
    await waitFor(() => expect(hlsState.loadSource).toHaveBeenCalledWith('/api/recordings/rec-1/play/master.m3u8'))
    const video = view.container.querySelector('video')!
    const pause = vi.spyOn(video, 'pause').mockImplementation(() => undefined)
    const load = vi.spyOn(video, 'load').mockImplementation(() => undefined)
    view.unmount()
    expect(hlsState.destroy).toHaveBeenCalledTimes(1)
    expect(video.hasAttribute('src')).toBe(false)
    expect(pause).toHaveBeenCalled()
    expect(load).toHaveBeenCalled()
  })
})
