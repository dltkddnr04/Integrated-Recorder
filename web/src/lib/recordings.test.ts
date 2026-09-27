import { describe, expect, it } from 'vitest'
import { listResourceLabel, localDateBoundary, localDateInputValue } from './recordings'
import type { RecordingListItem } from '@/types/api'

const row = { resource_type: 'channel', resource_id: 'demo' } as RecordingListItem

describe('recording list wire helpers', () => {
  it('uses resource fields only when both are present', () => {
    expect(listResourceLabel(row)).toBe('channel / demo')
    expect(listResourceLabel({ resource_type: 'channel' } as RecordingListItem)).toBe('—')
    expect(listResourceLabel({ resource_id: 'demo' } as RecordingListItem)).toBe('—')
  })

  it('encodes browser-local calendar boundaries with the local offset', () => {
    const start = localDateBoundary('2026-09-27')!
    const end = localDateBoundary('2026-09-27', true)!
    const expectedOffset = (() => {
      const offset = -new Date(2026, 8, 27).getTimezoneOffset()
      const sign = offset >= 0 ? '+' : '-'
      const abs = Math.abs(offset)
      return `${sign}${String(Math.floor(abs / 60)).padStart(2, '0')}:${String(abs % 60).padStart(2, '0')}`
    })()
    expect(start).toBe(`2026-09-27T00:00:00.000${expectedOffset}`)
    expect(end).toBe(`2026-09-27T23:59:59.999${expectedOffset}`)
    expect(localDateBoundary('not-a-date')).toBeUndefined()
    expect(localDateInputValue(start)).toBe('2026-09-27')
  })
})
