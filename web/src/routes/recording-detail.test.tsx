import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Timeline } from './recording-detail'

describe('recording capture timeline', () => {
  it('orders each track by source epoch and sequence, not by source sequence alone', () => {
    render(<Timeline
      segments={[
        { trackId: 'main', source_epoch: 1, sequence: 1, archive_ordinal: 0 },
        { trackId: 'main', source_epoch: 0, sequence: 99, archive_ordinal: 1 },
      ]}
      gaps={[{ track_id: 'main', source_epoch: 1, from_sequence: 2, to_sequence: 3 }]}
    />)

    expect(screen.getByLabelText(/트랙 main 수집 타임라인/)).toBeTruthy()
    const markers = screen.getAllByTitle(/^(세그먼트|누락)/).map(marker => marker.getAttribute('title'))
    expect(markers).toEqual([
      '세그먼트 · 세대 0 · 순번 99',
      '세그먼트 · 세대 1 · 순번 1',
      '누락 · 세대 1 · 순번 2–3',
    ])
    const gap = screen.getByTitle('누락 · 세대 1 · 순번 2–3')
    expect(gap).toHaveStyle({ flexGrow: '2' })
  })
})
