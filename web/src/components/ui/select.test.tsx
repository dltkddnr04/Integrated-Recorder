import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Select, SelectItem } from './select'

describe('Select', () => {
  it('renders native options and forwards the selected value', () => {
    const onValueChange = vi.fn()
    render(<Select aria-label="상태" value="ready" onValueChange={onValueChange}><SelectItem value="ready">준비됨</SelectItem><SelectItem value="disabled">사용 중지</SelectItem></Select>)
    const select = screen.getByRole('combobox', { name: '상태' })
    expect(select).toHaveValue('ready')
    fireEvent.change(select, { target: { value: 'disabled' } })
    expect(onValueChange).toHaveBeenCalledWith('disabled')
    expect(screen.getByRole('option', { name: '사용 중지' })).toBeInTheDocument()
  })
})
