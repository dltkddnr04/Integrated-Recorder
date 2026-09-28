import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { ArrowLeft, Clock3, Pause, Play, RefreshCw, Settings2 } from 'lucide-react'
import { adaptersAPI, watchesAPI, type WatchMutationBody } from '@/api'
import { adaptersQuery, dashboardQuery, qk, watchEventsQuery, watchQuery, watchRecordingsQuery } from '@/api/queries'
import { AdapterMark } from '@/components/adapter/adapter-mark'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import { PageHeading } from '@/components/page-heading'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Confirm } from '@/components/ui/confirm'
import { WatchForm } from '@/components/watch/watch-form'
import { WatchDeleteAction } from '@/components/watch/watch-delete-action'
import { WatchPreview } from '@/components/watch/watch-preview'
import { watchErrorLabel, watchEventLabel } from '@/lib/watches'
import { errorMessage } from '@/lib/errors'
import { formatDate, formatDuration, resourceLabel } from '@/lib/utils'
import { useToast } from '@/components/ui/use-toast'

export function WatchDetailPage() {
  const { watchId } = useParams({ from: '/watches/$watchId' })
  const [editing, setEditing] = useState(false)
  const watchOptions = watchQuery(watchId)
  const query = useQuery({ ...watchOptions, refetchInterval: editing ? false : watchOptions.refetchInterval })
  const watch = query.data
  const adapters = useQuery(adaptersQuery)
  const recordingsOptions = watchRecordingsQuery(watchId)
  const active = !editing && (watch?.state === 'recording' || (watch?.enabled && watch.state === 'starting'))
  const recordings = useQuery({ ...recordingsOptions, refetchInterval: active ? 5_000 : false, refetchIntervalInBackground: false })
  const eventsOptions = watchEventsQuery(watchId)
  const events = useQuery({ ...eventsOptions, refetchInterval: active ? 15_000 : false, refetchIntervalInBackground: false })
  const client = useQueryClient()
  const navigate = useNavigate()
  const { toast } = useToast()
  const pendingUpdateBody = useRef<WatchMutationBody | undefined>(undefined)
  const adapter = adapters.data?.find(item => item.status.id === watch?.adapter_id)
  const schema = useQuery({ queryKey: qk.schema(watch?.adapter_id ?? '', watch?.resource), queryFn: () => adaptersAPI.schema(watch!.adapter_id, watch?.resource), enabled: Boolean(editing && watch?.adapter_id && adapter?.descriptor), retry: false, staleTime: 60_000 })
  const action = useMutation({
    mutationFn: ({ kind }: { kind: 'enable' | 'disable' | 'check' }) => watchesAPI[kind](watchId),
    onSuccess: async (_result, variables) => {
      await invalidate(client, watchId)
      toast(variables.kind === 'check' ? '방송 상태 확인을 요청했습니다.' : variables.kind === 'enable' ? '자동 녹화를 활성화했습니다.' : '자동 녹화를 비활성화했습니다.')
    },
    onError: () => toast('요청을 완료하지 못했습니다.', '다시 시도해 주세요.', 'error'),
  })
  const update = useMutation({
    mutationFn: () => {
      const body = pendingUpdateBody.current
      pendingUpdateBody.current = undefined
      if (!body) throw new Error('Watch 입력을 사용할 수 없습니다.')
      return watchesAPI.update(watchId, body)
    },
    onSuccess: async () => { setEditing(false); await invalidate(client, watchId); toast('Watch 설정을 저장했습니다.') },
    gcTime: 0,
    onError: () => toast('Watch 설정을 저장하지 못했습니다.', '입력 내용을 확인한 뒤 다시 시도해 주세요.', 'error'),
  })
  const remove = useMutation({
    mutationFn: () => watchesAPI.remove(watchId),
    onSuccess: async () => {
      await Promise.all([client.invalidateQueries({ queryKey: qk.watches }), client.invalidateQueries({ queryKey: dashboardQuery.queryKey })])
      toast('Watch를 삭제했습니다. 기존 녹화는 보관되어 있습니다.')
      await navigate({ to: '/watches' })
    },
    onError: () => toast('Watch를 삭제하지 못했습니다.', '다시 시도해 주세요.', 'error'),
  })

  if (query.isLoading) return <LoadingState label="Watch 정보를 불러오는 중입니다" />
  if (query.error || !watch) return <ErrorState message={query.error ? errorMessage(query.error) : 'Watch를 찾을 수 없습니다.'} retry={() => void query.refetch()} />
  const displayName = watch.title || watch.resource?.display_name || adapter?.descriptor?.name || watch.adapter_name || watch.adapter_id

  return <div className="page-enter">
    <PageHeading eyebrow="자동 녹화" title={displayName} description={`Watch ID · ${watch.id}`} actions={<><Link to="/watches"><Button variant="outline"><ArrowLeft className="h-4 w-4" />목록</Button></Link><Button variant="outline" onClick={() => void query.refetch()}><RefreshCw className="h-4 w-4" />새로고침</Button><Button variant="outline" onClick={() => setEditing(value => !value)}><Settings2 className="h-4 w-4" />{editing ? '편집 닫기' : '편집'}</Button>{watch.enabled ? <Confirm trigger={<Button variant="outline" disabled={action.isPending}><Pause className="h-4 w-4" />감시 중지</Button>} title="자동 녹화를 중지할까요?" description="새 방송을 감지하지 않도록 Watch를 비활성화합니다. 현재 진행 중인 녹화와 기존 보관 데이터는 그대로 유지됩니다." confirmLabel="감시 중지" onConfirm={() => action.mutate({ kind: 'disable' })} disabled={action.isPending} /> : <Button variant="outline" disabled={action.isPending} onClick={() => action.mutate({ kind: 'enable' })}><Play className="h-4 w-4" />활성화</Button>}<Button variant="outline" disabled={action.isPending || !watch.enabled} onClick={() => action.mutate({ kind: 'check' })}><RefreshCw className="h-4 w-4" />지금 확인</Button><WatchDeleteAction onDelete={() => remove.mutate()} disabled={remove.isPending} /></>} />
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_330px]">
      <div className="space-y-4">
        <Card><CardHeader className="flex-row items-center gap-3"><AdapterMark adapter={adapter} adapterId={watch.adapter_id} size="md" /><div className="min-w-0 flex-1"><CardTitle className="truncate">{adapter?.descriptor?.name ?? watch.adapter_name ?? watch.adapter_id}</CardTitle><p className="mt-1 truncate text-xs text-muted-foreground">{resourceLabel(watch.resource)}</p></div><StatusBadge state={watch.enabled ? watch.state : 'disabled'} /></CardHeader><CardContent>
          {watch.current_recording_id && <div className="mb-4 flex flex-wrap items-center gap-4 rounded-md border border-border bg-muted/20 p-3"><WatchPreview watch={watch} className="w-36" /><div className="min-w-0 flex-1"><p className="text-xs text-muted-foreground">현재 녹화</p><p className="mt-1"><StatusBadge state={watch.current_recording_state ?? 'recording'} /></p><Link to="/recordings/$recordingId" params={{ recordingId: watch.current_recording_id }} className="mt-2 inline-block text-sm font-medium text-primary hover:underline">녹화 상세 열기</Link></div></div>}
          <div className="grid gap-x-5 gap-y-4 sm:grid-cols-2 lg:grid-cols-3"><Info label="확인 주기" value={`${watch.check_interval_seconds}초`} /><Info label="마지막 확인" value={formatDate(watch.last_checked_at)} /><Info label="다음 확인" value={watch.enabled ? formatDate(watch.next_check_at) : '중지됨'} /><Info label="장면 미리보기" value={watch.preview_mode === 'segment' ? '생성' : '사용 안 함'} /><Info label="생성일" value={formatDate(watch.created_at)} /><Info label="수정일" value={formatDate(watch.updated_at)} /></div>
          {watch.last_error_code && <p className="mt-4 rounded-md bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">{watchErrorLabel(watch.last_error_code)}</p>}
        </CardContent></Card>
        {editing && <Card><CardHeader><CardTitle>Watch 설정 편집</CardTitle><p className="text-xs text-muted-foreground">비밀 값은 다시 표시되지 않습니다. 새 값을 입력해 교체하거나 설정된 값을 지울 수 있습니다.</p></CardHeader><CardContent>{!adapter?.descriptor ? <EmptyState title="어댑터 설명을 사용할 수 없습니다." description="어댑터가 준비되면 설정을 편집할 수 있습니다." /> : schema.isLoading ? <LoadingState /> : schema.error || !schema.data ? <ErrorState message={schema.error ? errorMessage(schema.error) : '입력 양식을 사용할 수 없습니다.'} retry={() => void schema.refetch()} /> : <WatchForm key={watch.id} adapter={adapter} schema={schema.data.input_schema} watch={watch} submitLabel={update.isPending ? '저장 중…' : '변경 내용 저장'} busy={update.isPending} onSubmit={value => { pendingUpdateBody.current = value; update.mutate() }} />}</CardContent></Card>}
        <Card><CardHeader><CardTitle>최근 자동 녹화</CardTitle><p className="text-xs text-muted-foreground">이 Watch에서 시작된 최근 녹화입니다. Watch 삭제 후에도 녹화는 별도로 보관됩니다.</p></CardHeader><CardContent>{recordings.isLoading ? <LoadingState /> : recordings.error ? <p role="alert" className="text-sm text-muted-foreground">녹화 목록을 불러오지 못했습니다.</p> : recordings.data?.items.length ? <div className="divide-y divide-border">{recordings.data.items.map(recording => <Link key={recording.id} to="/recordings/$recordingId" params={{ recordingId: recording.id }} className="flex min-w-0 items-center gap-3 py-3"><StatusBadge state={recording.state} /><span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{recording.title || recording.id}</span><span className="mt-1 block truncate text-xs text-muted-foreground">{formatDate(recording.started_at)} · {formatDuration(recording.duration_seconds)}</span></span><span className="shrink-0 text-xs text-muted-foreground">{recording.segment_count ?? 0} 세그먼트</span></Link>)}</div> : <EmptyState title="아직 생성된 녹화가 없습니다." />}{recordings.data?.truncated && <p className="mt-3 text-[11px] text-muted-foreground">오래된 자동 녹화 연결은 보관 한도{recordings.data.retention_limit ? ` ${recordings.data.retention_limit.toLocaleString('ko-KR')}개` : ''}에 따라 목록에서 제외될 수 있습니다. 녹화 보관 데이터는 유지됩니다.</p>}</CardContent></Card>
      </div>
      <Card><CardHeader><CardTitle className="flex items-center gap-2"><Clock3 className="h-4 w-4" />Watch 기록</CardTitle><p className="text-xs text-muted-foreground">최근 상태 변경과 자동 녹화 이벤트입니다.</p></CardHeader><CardContent>{events.isLoading ? <LoadingState /> : events.error ? <p role="alert" className="text-sm text-muted-foreground">기록을 불러오지 못했습니다.</p> : events.data?.items.length ? <ol className="max-h-[650px] divide-y divide-border overflow-auto">{events.data.items.map(event => <li key={event.id} className="flex items-start gap-3 py-3"><span className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-primary" /><div className="min-w-0 flex-1"><p className="text-sm font-medium">{watchEventLabel(event)}</p>{event.state && <p className="mt-1"><StatusBadge state={event.state} /></p>}{event.recording_id && <Link to="/recordings/$recordingId" params={{ recordingId: event.recording_id }} className="mt-1 block truncate text-xs text-primary hover:underline">녹화 {event.recording_id}</Link>}<time className="mt-1 block text-[10px] text-muted-foreground">{formatDate(event.at)}</time></div></li>)}</ol> : <EmptyState title="기록된 이벤트가 없습니다." />}</CardContent></Card>
    </div>
  </div>
}

function Info({ label, value }: { label: string; value: string }) { return <div><p className="text-[11px] text-muted-foreground">{label}</p><p className="mt-1 break-all text-sm font-medium">{value}</p></div> }

async function invalidate(client: ReturnType<typeof useQueryClient>, id: string) {
  await Promise.all([
    client.invalidateQueries({ queryKey: qk.watches }),
    client.invalidateQueries({ queryKey: dashboardQuery.queryKey }),
    client.invalidateQueries({ queryKey: qk.watch(id) }),
    client.invalidateQueries({ queryKey: qk.watchRecordings(id) }),
    client.invalidateQueries({ queryKey: qk.watchEvents(id) }),
  ])
}
