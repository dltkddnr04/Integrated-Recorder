import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ExportRow } from './recording-detail'
import type { ExportJob } from '@/types/api'

const job = (state: string): ExportJob => ({ id: 'export-1', recording_id: 'recording-1', state, format: 'mkv' })

describe('export row action semantics', () => {
  it('cancels active jobs without presenting archive deletion copy', () => {
    const action = vi.fn()
    render(<ExportRow item={job('running')} onDelete={action} deleting={false} />)
    fireEvent.click(screen.getByRole('button', { name: 'Export 작업 취소' }))
    expect(action).toHaveBeenCalledOnce()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('requires confirmation to delete a terminal export projection', () => {
    const action = vi.fn()
    render(<ExportRow item={job('failed')} onDelete={action} deleting={false} />)
    fireEvent.click(screen.getByRole('button', { name: 'export 항목 삭제' }))
    expect(screen.getByRole('alertdialog')).toHaveTextContent('Canonical recording archive에는 영향이 없습니다.')
    fireEvent.click(screen.getByRole('button', { name: 'Export 삭제' }))
    expect(action).toHaveBeenCalledOnce()
  })
})
