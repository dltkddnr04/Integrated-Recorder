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

    expect(screen.getByLabelText(/Track main capture timeline/)).toBeTruthy()
    const markers = screen.getAllByTitle(/^(Segment|Gap)/).map(marker => marker.getAttribute('title'))
    expect(markers).toEqual([
      'Segment · epoch 0 · sequence 99',
      'Segment · epoch 1 · sequence 1',
      'Gap · epoch 1 · 2–3',
    ])
  })
})
