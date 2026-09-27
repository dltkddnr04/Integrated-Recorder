import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Confirm } from './confirm'

describe('Confirm', () => {
  it('requires explicit confirmation before calling destructive actions', async () => {
    const onConfirm = vi.fn()
    render(<Confirm trigger={<button>Delete archive</button>} title="Delete recording?" description="Canonical archive will be deleted." confirmLabel="Delete" destructive onConfirm={onConfirm} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete archive' }))
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    expect(onConfirm).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1))
  })
})
