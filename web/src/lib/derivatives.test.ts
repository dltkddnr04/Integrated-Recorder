import { describe, expect, it } from 'vitest'
import { isFFmpegAvailable } from './derivatives'

describe('FFmpeg-backed derivative availability', () => {
  it('requires an explicit available response for both export and thumbnail controls', () => {
    expect(isFFmpegAvailable({ available: true })).toBe(true)
    expect(isFFmpegAvailable({ available: false })).toBe(false)
    expect(isFFmpegAvailable(undefined)).toBe(false)
  })
})
