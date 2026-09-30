import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Download, RotateCcw, Search, Sparkles } from 'lucide-react'
import { runtimeUpdateAPI } from '@/api'
import { qk, runtimeUpdateQuery } from '@/api/queries'
import { errorMessage } from '@/lib/errors'
import type { RuntimeGenerationSummary, RuntimeReleaseSummary, RuntimeUpdateStatus } from '@/types/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ErrorState, LoadingState } from '@/components/query-state'

type RuntimeUpdateAction = 'check' | 'stage' | 'activate' | 'rollback'

const verificationLabels: Record<RuntimeUpdateStatus['verification_state'], string> = {
  unknown: '상태 알 수 없음', not_checked: '아직 확인하지 않음', checking: '확인 중', verified: '검증 완료', failed: '검증 실패',
}
const generationStateLabels: Record<string, string> = {
  staging: '준비 중', verified: '검증 완료', ready: '활성화 준비 완료', active: '활성', draining: '정리 중', retired: '종료', failed: '실패',
}
const releaseChannelLabels: Record<string, string> = { stable: '안정판', prerelease: '사전 공개', development: '개발' }
const unavailableMessages: Record<string, string> = {
  development_build: '개발 빌드에서는 애플리케이션 업데이트를 사용할 수 없습니다.',
  source_unavailable: '업데이트 제공처를 사용할 수 없습니다.',
  unsupported_platform: '현재 플랫폼용 업데이트를 제공하지 않습니다.',
  trust_key_unavailable: '릴리스 검증 키를 사용할 수 없어 업데이트를 진행할 수 없습니다.',
  updates_disabled: '이 환경에서는 업데이트가 비활성화되어 있습니다.',
  host_update_required: '애플리케이션 업데이트 전에 Runtime Host 업데이트가 필요합니다.',
}
const failureMessages: Record<string, string> = {
  update_unavailable: '업데이트 기능을 사용할 수 없습니다.', update_check_failed: '업데이트 확인에 실패했습니다.',
  no_update_available: '사용 가능한 업데이트가 없습니다.', operation_conflict: '다른 업데이트 작업이 진행 중입니다.',
  verification_failed: '릴리스 검증에 실패했습니다.', candidate_not_ready: '활성화할 후보 릴리스가 준비되지 않았습니다.',
  release_incompatible: '현재 실행 환경과 호환되지 않는 릴리스입니다.', stage_failed: '릴리스를 준비하지 못했습니다.',
  activation_failed: '릴리스를 활성화하지 못했습니다.', rollback_unavailable: '롤백할 이전 릴리스가 없습니다.',
  rollback_failed: '이전 릴리스로 롤백하지 못했습니다.', internal_error: '업데이트 요청을 처리하지 못했습니다.',
}

export function RuntimeUpdates() {
  const client = useQueryClient()
  const status = useQuery({ ...runtimeUpdateQuery, enabled: true })
  const action = useMutation({
    mutationFn: (operation: RuntimeUpdateAction) => runtimeUpdateAPI[operation](),
    onSuccess: result => { client.setQueryData(qk.runtimeUpdate, result) },
  })
  const value = status.data
  const unavailableReason = value?.update_unavailable_reason
  const unavailable = unavailableReason ? unavailableMessages[unavailableReason] ?? '이 환경에서는 업데이트를 사용할 수 없습니다.' : undefined
  const developmentBuild = value?.application.version === 'dev'
  const canUpdate = Boolean(value && !unavailable && !developmentBuild)
  const pending = action.isPending
  const perform = (operation: RuntimeUpdateAction) => action.mutate(operation)

  return <div role="tabpanel" id="settings-panel-updates" aria-labelledby="settings-tab-updates" tabIndex={0} className="space-y-4">
    <Card>
      <CardHeader><CardTitle className="flex items-center gap-2"><Sparkles className="h-4 w-4 text-primary" />애플리케이션 업데이트</CardTitle><p className="text-xs leading-5 text-muted-foreground">새 버전을 검증하고 활성화합니다. 진행 중인 녹화는 시작 당시의 엔진 세대에서 계속됩니다.</p></CardHeader>
      <CardContent>
        {status.isLoading ? <LoadingState /> : status.error || !value ? <ErrorState message={status.error ? errorMessage(status.error) : '업데이트 상태를 불러올 수 없습니다.'} retry={() => void status.refetch()} /> : <div className="space-y-5">
          <div className="grid gap-3 sm:grid-cols-2">
            <IdentityCard title="현재 애플리케이션" identity={value.application} />
            <IdentityCard title="Runtime Host" identity={value.host} />
          </div>

          <div className="grid gap-3 lg:grid-cols-3">
            <ReleaseCard title="사용 가능한 버전" release={value.available_release} empty="새 버전을 확인하지 않았습니다." />
            <ReleaseCard title="준비된 버전" release={value.staged_release} empty="다운로드한 후보가 없습니다." />
            <ReleaseCard title="롤백 버전" release={value.previous_release} empty="이전 버전이 없습니다." />
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={value.verification_state === 'verified' ? 'green' : value.verification_state === 'failed' ? 'red' : value.verification_state === 'checking' ? 'amber' : 'neutral'}>검증 상태 · {verificationLabels[value.verification_state]}</Badge>
            {value.updates_available && <Badge tone="blue">새 버전 있음</Badge>}
            {value.last_failure_code && <span className="text-xs text-destructive">{failureMessages[value.last_failure_code] ?? '마지막 업데이트 작업에 실패했습니다.'}</span>}
          </div>

          {unavailable && <p className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-xs leading-5 text-amber-800 dark:text-amber-200">{unavailable}</p>}
          {!unavailable && developmentBuild && <p className="rounded-md border border-muted bg-muted/40 p-3 text-xs leading-5 text-muted-foreground">개발 빌드에서는 애플리케이션 업데이트를 사용할 수 없습니다.</p>}
          {action.error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs leading-5 text-destructive">{errorMessage(action.error)}</p>}

          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => perform('check')} disabled={!canUpdate || pending}><Search className="h-3.5 w-3.5" />업데이트 확인</Button>
            <Button variant="outline" size="sm" onClick={() => perform('stage')} disabled={!canUpdate || !value.available_release || pending}><Download className="h-3.5 w-3.5" />다운로드 및 검증</Button>
            <Button size="sm" onClick={() => perform('activate')} disabled={!canUpdate || !value.staged_release || value.verification_state !== 'verified' || pending}><Check className="h-3.5 w-3.5" />활성화</Button>
            <Button variant="outline" size="sm" onClick={() => perform('rollback')} disabled={!canUpdate || !value.previous_release || pending}><RotateCcw className="h-3.5 w-3.5" />이전 버전으로 롤백</Button>
          </div>
          {pending && <p role="status" className="text-xs text-muted-foreground">업데이트 작업을 진행하고 있습니다…</p>}
          <p className="text-xs leading-5 text-muted-foreground">활성화해도 진행 중인 녹화는 중단되지 않습니다. 기존 녹화는 현재 엔진 세대에서 마칠 때까지 유지되고, 새 녹화부터 새 기본 엔진을 사용합니다.</p>
        </div>}
      </CardContent>
    </Card>

    {value && <Card>
      <CardHeader><CardTitle>실행 중인 세대</CardTitle><p className="text-xs text-muted-foreground">각 세대에 고정된 녹화는 해당 세대가 종료될 때까지 계속됩니다.</p></CardHeader>
      <CardContent><GenerationList active={value.active_generations ?? []} draining={value.draining_generations ?? []} activeControl={value.active_control} defaultEngine={value.default_engine} /></CardContent>
    </Card>}
  </div>
}

function IdentityCard({ title, identity }: { title: string; identity: RuntimeUpdateStatus['application'] }) {
  return <div className="min-w-0 rounded-md border border-border p-3"><p className="text-xs text-muted-foreground">{title}</p><p className="mt-1 truncate text-base font-semibold" title={identity.version}>{identity.version}</p><p className="mt-1 break-all font-mono text-[11px] text-muted-foreground">{identity.commit}</p><p className="mt-1 text-xs text-muted-foreground">채널 {releaseChannelLabels[identity.release_channel] ?? '기타'} · 프로토콜 {identity.runtime_protocol_version}</p></div>
}

function ReleaseCard({ title, release, empty }: { title: string; release?: RuntimeReleaseSummary; empty: string }) {
	return <div className="min-w-0 rounded-md border border-border p-3"><p className="text-xs text-muted-foreground">{title}</p>{release ? <><p className="mt-1 truncate text-sm font-semibold" title={release.version}>{release.version}</p><p className="mt-1 break-all font-mono text-[11px] text-muted-foreground">{release.commit}</p><p className="mt-1 text-xs text-muted-foreground">채널 {releaseChannelLabels[release.release_channel] ?? '기타'} · 빌드 {release.build_time}</p>{release.notes_summary && <div className="mt-2 border-t border-border pt-2"><p className="text-[11px] font-medium text-muted-foreground">릴리스 요약</p><p className="mt-1 line-clamp-3 whitespace-normal text-xs leading-5 text-foreground">{release.notes_summary}</p></div>}</> : <p className="mt-2 text-xs text-muted-foreground">{empty}</p>}</div>
}

function GenerationList({ active, draining, activeControl, defaultEngine }: {
  active: RuntimeGenerationSummary[]; draining: RuntimeGenerationSummary[]
  activeControl?: RuntimeGenerationSummary; defaultEngine?: RuntimeGenerationSummary
}) {
  const rows = [...active.map(item => ({ ...item, displayState: generationStateLabels[item.state] ?? '상태 알 수 없음' })), ...draining.map(item => ({ ...item, displayState: generationStateLabels[item.state] ?? '상태 알 수 없음' }))]
  for (const item of [activeControl, defaultEngine]) {
    if (item && !rows.some(row => row.id === item.id)) rows.push({ ...item, displayState: generationStateLabels[item.state] ?? '상태 알 수 없음' })
  }
  if (!rows.length) return <p className="text-sm text-muted-foreground">실행 중인 세대가 없습니다.</p>
  return <div className="divide-y divide-border">{rows.map(item => <div key={item.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 text-xs"><span className="min-w-0 flex-1 truncate font-medium">{item.version} <span className="font-normal text-muted-foreground">· {item.displayState}</span></span><span className="text-muted-foreground">녹화 {item.active_recordings}개</span><Badge tone={item.id === defaultEngine?.id ? 'blue' : item.state === 'draining' ? 'amber' : 'neutral'}>{item.id === defaultEngine?.id ? '기본 엔진' : generationStateLabels[item.state] ?? '상태 알 수 없음'}</Badge></div>)}</div>
}
