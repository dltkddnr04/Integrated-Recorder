import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { Activity, BellRing, Check, Clock3, Pause, Play, Plus, Radio, RefreshCw } from 'lucide-react'
import { watchesAPI } from '@/api'
import { adaptersQuery, watchesQuery } from '@/api/queries'
import { AdapterMark } from '@/components/adapter/adapter-mark'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import { PageHeading } from '@/components/page-heading'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Confirm } from '@/components/ui/confirm'
import { WatchPreview } from '@/components/watch/watch-preview'
import { invalidateWatchData, watchErrorLabel } from '@/lib/watches'
import { formatDate, resourceLabel } from '@/lib/utils'
import { useToast } from '@/components/ui/use-toast'
import type { Adapter, WatchView } from '@/types/api'
import { errorMessage } from '@/lib/errors'

export function WatchesPage() {
  const query = useQuery(watchesQuery)
  const adapters = useQuery(adaptersQuery)
  const client = useQueryClient()
  const { toast } = useToast()
  const action = useMutation({
    mutationFn: ({ id, kind }: { id: string; kind: 'enable' | 'disable' | 'check' }) => watchesAPI[kind](id),
    onSuccess: async (watch, variables) => {
      await invalidateWatchData(client, watch.id)
      toast(variables.kind === 'check' ? '방송 상태를 확인하도록 요청했습니다.' : variables.kind === 'enable' ? '자동 녹화를 활성화했습니다.' : '자동 녹화를 비활성화했습니다.', undefined, 'success')
    },
    onError: (_error, variables) => toast(variables.kind === 'check' ? '상태 확인 요청 실패' : 'Watch 상태 변경 실패', '요청을 완료하지 못했습니다. 다시 시도해 주세요.', 'error'),
  })
  const items = query.data?.items ?? []
  const summary = {
    total: items.length,
    enabled: items.filter(item => item.enabled).length,
    recording: items.filter(item => item.state === 'recording').length,
    offline: items.filter(item => item.enabled && item.state === 'offline').length,
    attention: items.filter(item => item.state === 'attention_required').length,
  }
  const adapterById = new Map((adapters.data ?? []).map(adapter => [adapter.status.id, adapter]))

  return <div className="page-enter">
    <PageHeading eyebrow="무인 녹화" title="자동 녹화" description="Watch를 등록하면 방송이 시작될 때 녹화를 만들고, 방송이 끝난 뒤 다음 회차를 계속 확인합니다." actions={<><Button variant="outline" onClick={() => void query.refetch()}><RefreshCw className="h-4 w-4" />새로고침</Button><Link to="/watches/new"><Button><Plus className="h-4 w-4" />Watch 등록</Button></Link></>} />
    {!query.isLoading && !query.error && <div className="mb-5 grid grid-cols-2 gap-3 sm:grid-cols-5"><Summary label="등록" value={summary.total} icon={<Activity className="h-4 w-4" />} /><Summary label="활성" value={summary.enabled} icon={<Check className="h-4 w-4" />} /><Summary label="녹화 중" value={summary.recording} icon={<Radio className="h-4 w-4" />} /><Summary label="오프라인" value={summary.offline} icon={<Clock3 className="h-4 w-4" />} /><Summary label="주의 필요" value={summary.attention} icon={<BellRing className="h-4 w-4" />} /></div>}
    {query.isLoading ? <LoadingState label="자동 녹화 Watch를 불러오는 중입니다" /> : query.error ? <ErrorState message={errorMessage(query.error)} retry={() => void query.refetch()} /> : items.length ? <div className="overflow-hidden rounded-lg border border-border bg-card"><div className="hidden grid-cols-[minmax(260px,1.3fr)_minmax(120px,.7fr)_minmax(170px,.9fr)_minmax(145px,.8fr)_auto] items-center gap-4 border-b border-border bg-muted/40 px-4 py-2.5 text-[10px] font-semibold text-muted-foreground md:grid"><span>Watch</span><span>상태</span><span>현재 녹화</span><span>다음 확인</span><span className="text-right">동작</span></div><div className="divide-y divide-border">{items.map(watch => <WatchRow key={watch.id} watch={watch} adapter={adapterById.get(watch.adapter_id)} busy={action.isPending} onAction={kind => action.mutate({ id: watch.id, kind })} />)}</div></div> : <EmptyState title="등록된 자동 녹화가 없습니다." description="Watch를 등록하면 서버가 방송 상태를 감시하고 새 회차를 자동으로 녹화합니다." />}
  </div>
}

function WatchRow({ watch, adapter, busy, onAction }: { watch: WatchView; adapter?: Adapter; busy: boolean; onAction: (kind: 'enable' | 'disable' | 'check') => void }) {
  const title = watch.title || watch.resource?.display_name || adapter?.descriptor?.name || watch.adapter_name || watch.adapter_id
  const identity = <span className="min-w-0 flex-1"><span className="block truncate text-sm font-semibold" title={title}>{title}</span><span className="mt-1 block truncate text-xs text-muted-foreground">{resourceLabel(watch.resource)}</span><span className="mt-1 block text-[11px] text-muted-foreground">{watch.check_interval_seconds}초마다 확인 · 미리보기 {watch.preview_mode === 'segment' ? '사용' : '사용 안 함'}</span></span>
  return <article className="grid min-w-0 gap-3 px-4 py-3.5 md:grid-cols-[minmax(260px,1.3fr)_minmax(120px,.7fr)_minmax(170px,.9fr)_minmax(145px,.8fr)_auto] md:items-center md:gap-4">
    <div className="flex min-w-0 items-center gap-3"><WatchPreview watch={watch} className="w-24 md:w-28" /><AdapterMark adapter={adapter} adapterId={watch.adapter_id} size="sm" />{identity}</div>
    <div className="flex items-center gap-2 md:block"><StatusBadge state={watch.enabled ? watch.state : 'disabled'} />{watch.last_error_code && <p className="mt-1 text-[10px] text-amber-700 dark:text-amber-300">{watchErrorLabel(watch.last_error_code)}</p>}</div>
    <div className="min-w-0">{watch.current_recording_id ? <Link to="/recordings/$recordingId" params={{ recordingId: watch.current_recording_id }} className="block truncate text-sm font-medium text-primary hover:underline">{watch.current_recording_state ? <StatusBadge state={watch.current_recording_state} /> : '현재 녹화 열기'}</Link> : <span className="text-xs text-muted-foreground">진행 중인 녹화 없음</span>}</div>
    <div className="text-xs text-muted-foreground"><span className="block">마지막 확인 · {formatDate(watch.last_checked_at)}</span><span className="mt-1 block">다음 확인 · {watch.enabled ? formatDate(watch.next_check_at) : '중지됨'}</span></div>
    <div className="flex flex-wrap items-center justify-start gap-1.5 md:justify-end"><Button size="sm" variant="outline" onClick={() => onAction('check')} disabled={busy || !watch.enabled} aria-label={`${title} 지금 확인`}><RefreshCw className="h-3.5 w-3.5" />확인</Button>{watch.enabled ? <Confirm trigger={<Button size="sm" variant="outline" disabled={busy}><Pause className="h-3.5 w-3.5" />중지</Button>} title="자동 녹화를 중지할까요?" description="이 Watch의 새 방송 감시만 중지합니다. 이미 진행 중인 녹화는 계속되며, 녹화 화면에서 별도로 중지할 수 있습니다." confirmLabel="감시 중지" onConfirm={() => onAction('disable')} disabled={busy} /> : <Button size="sm" variant="outline" onClick={() => onAction('enable')} disabled={busy}><Play className="h-3.5 w-3.5" />활성화</Button>}<Link to="/watches/$watchId" params={{ watchId: watch.id }}><Button size="sm" variant="ghost">상세</Button></Link></div>
    <p className="text-[10px] text-muted-foreground md:hidden">{adapter?.descriptor?.name ?? watch.adapter_name ?? watch.adapter_id} · 마지막 확인 {formatDate(watch.last_checked_at)}</p>
  </article>
}

function Summary({ label, value, icon }: { label: string; value: number; icon: ReactNode }) { return <Card><CardContent className="flex min-h-[76px] items-center justify-between gap-2 py-3"><span><span className="block text-[10px] text-muted-foreground">{label}</span><span className="mt-1 block text-xl font-semibold tabular-nums">{value}</span></span><span className="text-muted-foreground">{icon}</span></CardContent></Card> }
