import { describe, expect, it } from 'vitest'
import { isRecordingState, recordingStates } from './api'

describe('recording states', () => {
  it('matches the canonical backend state set', () => {
    expect(recordingStates).toEqual(['recording', 'stopped', 'completed', 'interrupted'])
    for (const state of recordingStates) expect(isRecordingState(state)).toBe(true)
    expect(isRecordingState('failed')).toBe(false)
  })
})
