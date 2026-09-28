import type { Adapter, WatchEvent } from '@/types/api'
import type { QueryClient } from '@tanstack/react-query'
import { dashboardQuery, qk } from '@/api/queries'

export function watchCapableAdapters(adapters: readonly Adapter[] | undefined): Adapter[] {
  return (adapters ?? []).filter(adapter => adapter.status.state === 'ready' && adapter.descriptor?.capabilities?.includes('watch'))
}

const eventLabels: Record<string, string> = {
  watch_created: '자동 녹화 등록', watch_updated: '설정 변경', watch_enabled: '감시 시작', watch_disabled: '감시 중지',
  live_detected: '방송 감지', recording_started: '자동 녹화 시작', recording_ended: '자동 녹화 종료',
  check_failed: '확인 실패', attention_required: '주의 필요', watch_deleted: '자동 녹화 삭제', manual_watch_check: '수동 확인 요청',
}

export function watchEventLabel(event: WatchEvent): string {
  return eventLabels[event.type] ?? '상태 변경'
}

export function watchErrorLabel(code?: string): string | undefined {
  if (!code) return undefined
  const labels: Record<string, string> = {
    adapter_unavailable: '어댑터를 사용할 수 없습니다.', adapter_disabled: '어댑터가 사용 중지되었습니다.',
    adapter_restarting: '어댑터가 재시작 중입니다.', adapter_timeout: '어댑터 응답 시간이 초과되었습니다.',
    authentication_required: '어댑터 인증 설정이 필요합니다.', interaction_required: '어댑터 설정에서 추가 확인이 필요합니다.',
    check_failed: '방송 상태를 확인하지 못했습니다.', resolve_failed: '방송 소스를 준비하지 못했습니다.',
  }
  return labels[code] ?? '최근 확인에 문제가 있었습니다.'
}

export async function invalidateWatchData(client: QueryClient, id?: string) {
  await Promise.all([
    client.invalidateQueries({ queryKey: qk.watches }),
    client.invalidateQueries({ queryKey: dashboardQuery.queryKey }),
    ...(id ? [client.invalidateQueries({ queryKey: qk.watch(id) })] : []),
  ])
}
