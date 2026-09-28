import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { AdapterMark } from './adapter-mark'
import type { Adapter } from '@/types/api'

const adapter = (icon_url?: string): Adapter => ({ status: { id: 'fixture', name: 'A third-party adapter with a very long display name', state: 'ready' }, descriptor: icon_url ? { id: 'fixture', name: 'A third-party adapter with a very long display name', version: '1', protocol_version: 1, input_schema: { fields: [] }, configuration_schema: { fields: [] }, media_types: [], branding: { icon_url } } : undefined })

describe('AdapterMark', () => {
  it('renders the descriptor branding URL as an image', () => {
    const { container } = render(<AdapterMark adapter={adapter('/api/adapters/fixture/icon')} showName />)
    expect(container.querySelector('img')).toHaveAttribute('src', '/api/adapters/fixture/icon')
    expect(screen.getByText(/third-party adapter/)).toBeInTheDocument()
  })

  it('uses the generic mark when branding is omitted', () => {
    const { container } = render(<AdapterMark adapter={adapter()} showName />)
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(container.querySelector('svg')).toBeInTheDocument()
  })

  it('falls back when the image cannot load', () => {
    const { container } = render(<AdapterMark adapter={adapter('/api/adapters/fixture/icon')} />)
    fireEvent.error(container.querySelector('img')!)
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(container.querySelector('svg')).toBeInTheDocument()
  })

  it('keeps the fixed mark and truncates a long display name', () => {
    const { container } = render(<div className="w-32"><AdapterMark adapter={adapter()} showName /></div>)
    expect(container.querySelector('.shrink-0.aspect-square')).toBeInTheDocument()
    expect(screen.getByText(/third-party adapter/)).toHaveClass('truncate')
  })
})
