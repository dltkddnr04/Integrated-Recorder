import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useDebouncedValue } from './use-debounced-value'

afterEach(() => vi.useRealTimers())

describe('useDebouncedValue', () => {
  it('publishes a changed search value once after the debounce window', () => {
    vi.useFakeTimers()
    const { result, rerender } = renderHook(({ value }) => useDebouncedValue(value, 300), { initialProps: { value: '' } })
    rerender({ value: 'one' })
    act(() => vi.advanceTimersByTime(200))
    rerender({ value: 'concert' })
    act(() => vi.advanceTimersByTime(299))
    expect(result.current).toBe('')
    act(() => vi.advanceTimersByTime(1))
    expect(result.current).toBe('concert')
  })
})
