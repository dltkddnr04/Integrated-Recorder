import { describe, expect, it } from 'vitest'
import { watchCapableAdapters, watchEventLabel, watchErrorLabel } from './watches'
import { watchStateLabel } from './labels'
import { watchDetailPollInterval, watchListPollInterval } from '@/api/queries'
import type { Adapter } from '@/types/api'

const adapters: Adapter[] = [
  { status: { id: 'owncast', state: 'ready' }, descriptor: { id: 'owncast', name: 'Owncast', version: '1', protocol_version: 1, capabilities: ['resolve', 'watch'], input_schema: { fields: [] }, configuration_schema: { fields: [] }, media_types: ['HLS'] } },
  { status: { id: 'legacy', state: 'ready' }, descriptor: { id: 'legacy', name: 'Legacy', version: '1', protocol_version: 1, capabilities: ['resolve'], input_schema: { fields: [] }, configuration_schema: { fields: [] }, media_types: ['HLS'] } },
  { status: { id: 'disabled-watch', state: 'disabled' }, descriptor: { id: 'disabled-watch', name: 'Disabled', version: '1', protocol_version: 1, capabilities: ['watch'], input_schema: { fields: [] }, configuration_schema: { fields: [] }, media_types: ['HLS'] } },
]

describe('Watch presentation helpers', () => {
  it('offers only ready adapters that declare the watch capability', () => {
    expect(watchCapableAdapters(adapters).map(adapter => adapter.status.id)).toEqual(['owncast'])
  })

  it('labels Watch states in Korean', () => {
    expect(['disabled', 'offline', 'checking', 'starting', 'recording', 'backoff', 'attention_required', 'suppressed'].map(watchStateLabel)).toEqual([
      '사용 중지', '오프라인', '확인 중', '녹화 시작 중', '녹화 중', '재시도 대기', '주의 필요', '현재 방송 제외',
    ])
  })

  it('does not expose raw error details in the UI and localizes known events', () => {
    expect(watchErrorLabel('adapter_unavailable')).toBe('어댑터를 사용할 수 없습니다.')
    expect(watchErrorLabel('https://private.example/secret')).toBe('최근 확인에 문제가 있었습니다.')
    expect(watchEventLabel({ id: 'event-1', type: 'live_detected', at: '2026-09-28T00:00:00Z' })).toBe('방송 감지')
  })

  it('keeps polling a recording after its Watch is disabled', () => {
    expect(watchDetailPollInterval({ state: 'recording', enabled: false })).toBe(4_000)
    expect(watchListPollInterval([{ state: 'recording' }, { state: 'disabled' }])).toBe(4_000)
    expect(watchDetailPollInterval({ state: 'disabled', enabled: false })).toBe(false)
  })
})
