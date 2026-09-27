import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { StatusBadge } from './status-badge'

describe('recording status labels', () => {
  it.each([
    ['recording', '녹화 중'], ['stopped', '중지됨'], ['completed', '완료'], ['interrupted', '중단됨'],
  ])('renders canonical state %s as %s', (state, label) => {
    render(<StatusBadge state={state} />)
    expect(screen.getByText(label)).toBeInTheDocument()
  })
})
