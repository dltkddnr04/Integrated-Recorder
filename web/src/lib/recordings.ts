import type { RecordingListItem } from '@/types/api'

export function listResourceLabel(item: Pick<RecordingListItem, 'resource_type' | 'resource_id'>): string {
  return item.resource_type && item.resource_id ? `${item.resource_type} / ${item.resource_id}` : '—'
}

/** Returns an RFC3339 boundary for a date selected in the browser's local calendar. */
export function localDateBoundary(date: string, endOfDay = false): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date)
  if (!match) return undefined
  const [, year, month, day] = match
  const value = endOfDay
    ? new Date(Number(year), Number(month) - 1, Number(day), 23, 59, 59, 999)
    : new Date(Number(year), Number(month) - 1, Number(day), 0, 0, 0, 0)
  if (value.getFullYear() !== Number(year) || value.getMonth() !== Number(month) - 1 || value.getDate() !== Number(day)) return undefined
  const offset = -value.getTimezoneOffset()
  const sign = offset >= 0 ? '+' : '-'
  const absOffset = Math.abs(offset)
  const tz = `${sign}${String(Math.floor(absOffset / 60)).padStart(2, '0')}:${String(absOffset % 60).padStart(2, '0')}`
  const local = `${year}-${month}-${day}T${String(value.getHours()).padStart(2, '0')}:${String(value.getMinutes()).padStart(2, '0')}:${String(value.getSeconds()).padStart(2, '0')}.${String(value.getMilliseconds()).padStart(3, '0')}`
  return `${local}${tz}`
}

export function localDateInputValue(value?: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}
