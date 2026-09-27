import { describe, expect, it } from 'vitest'
import { APIError } from '@/api/client'
import { errorMessage } from './errors'

describe('errorMessage', () => {
  it('shows safe message text without Error prefixes or object dumps', () => {
    expect(errorMessage(new Error('server unavailable'))).toBe('server unavailable')
    expect(errorMessage(new APIError(409, 'conflict', 'req-1'))).toBe('conflict')
    expect(errorMessage({ secret: 'never show' })).toContain('요청을 완료하지 못했습니다')
    expect(errorMessage({ secret: 'never show' })).not.toContain('never show')
  })
})
