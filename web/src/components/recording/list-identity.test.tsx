import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { RecordingListIdentity } from './list-identity'
import type { RecordingListItem } from '@/types/api'

const realListWireShape: RecordingListItem = {
  id: 'rec-1', title: 'Concert', adapter_id: 'owncast', adapter_name: 'Owncast', state: 'stopped',
  resource_type: 'channel', resource_id: 'demo', tags: ['important'], created_at: '2026-09-27T00:00:00Z',
  started_at: '2026-09-27T00:00:00Z', duration_seconds: 123, archive_size_bytes: 1234,
  media_payload_size_bytes: 1000, manifest_size_bytes: 100, init_payload_size_bytes: 134,
  segment_count: 4, init_segment_count: 1, manifest_snapshot_count: 3, gap_count: 0,
  gap_segment_count: 0, gap_duration_seconds: null, integrity: 'verified',
}

describe('recording list flat API contract', () => {
  it('renders adapter_name and top-level resource metadata from the list DTO', () => {
    render(<RecordingListIdentity item={realListWireShape} />)
    expect(screen.getByText('Owncast')).toBeTruthy()
    expect(screen.getByText('channel / demo')).toBeTruthy()
  })
})
