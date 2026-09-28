import { describe, expect, it } from 'vitest'
import { adapterStateLabel, integrityStatusLabel, recordingStateLabel, statusLabel, workflowStateLabel } from './labels'

describe('user-facing state labels', () => {
  it('translates canonical recording, integrity, adapter, workflow and job states', () => {
    expect(recordingStateLabel('stopped')).toBe('중지됨')
    expect(integrityStatusLabel('verified')).toBe('확인됨')
    expect(adapterStateLabel('ready')).toBe('준비됨')
    expect(workflowStateLabel('challenge_required')).toBe('입력 대기')
    expect(statusLabel('queued')).toBe('대기 중')
  })
})
