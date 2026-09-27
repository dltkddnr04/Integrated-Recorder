import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ErrorState } from './query-state'

describe('ErrorState', () => {
  it('shows request failures and offers a retry action', () => {
    const retry = vi.fn()
    render(<ErrorState message="연결을 확인하세요" retry={retry} />)
    expect(screen.getByRole('alert')).toHaveTextContent('연결을 확인하세요')
    fireEvent.click(screen.getByRole('button', { name: '다시 시도' }))
    expect(retry).toHaveBeenCalledTimes(1)
  })
})
