import { describe, expect, it } from 'vitest'
import { formatDate } from './utils'

describe('formatDate', () => {
  it('returns a placeholder for absent or invalid values', () => {
    expect(formatDate()).toBe('—')
    expect(formatDate(null)).toBe('—')
    expect(formatDate('not-a-date')).toBe('—')
  })

  it('uses compact Korean date and 24-hour time in the browser timezone', () => {
    const value = '2026-09-28T11:30:42.987Z'
    const expected = new Intl.DateTimeFormat('ko-KR', {
      year: 'numeric', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
    }).format(new Date(value))
    const actual = formatDate(value)

    expect(actual).toBe(expected)
    expect(actual).not.toMatch(/[A-Za-z]/)
    expect(actual).not.toMatch(/오전|오후|AM|PM/)
  })

  it('omits seconds consistently', () => {
    expect(formatDate('2026-09-28T11:30:01Z')).toBe(formatDate('2026-09-28T11:30:59Z'))
  })
})
