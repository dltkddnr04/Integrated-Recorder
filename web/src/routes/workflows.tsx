import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Clock3, Workflow } from 'lucide-react'
import { adaptersQuery, workflowsQuery, qk } from '@/api/queries'
import { workflowsAPI } from '@/api'
import { PageHeading } from '@/components/page-heading'
import { errorMessage } from '@/lib/errors'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import { formatDate, resourceLabel } from '@/lib/utils'
import { AdapterMark } from '@/components/adapter/adapter-mark'
import { workflowStateLabel } from '@/lib/labels'

export function WorkflowsPage() {
  const active = useQuery(workflowsQuery); const history = useQuery({ queryKey: qk.workflowHistory, queryFn: workflowsAPI.history, staleTime: 15_000 }); const adapters = useQuery(adaptersQuery)
  const adapterById = new Map((adapters.data ?? []).map(adapter => [adapter.status.id, adapter]))
  return <div className="page-enter"><PageHeading eyebrow="확인 진행 상황" title="워크플로" description="어댑터가 리소스를 찾고 필요한 정보를 요청하는 진행 중인 확인 작업입니다." actions={<Button variant="outline" onClick={() => { void active.refetch(); void history.refetch() }}>새로고침</Button>} />
    <div className="grid gap-4 xl:grid-cols-[1.1fr_.9fr]"><Card><CardHeader><CardTitle className="flex items-center gap-2"><Workflow className="h-4 w-4 text-primary" />진행 중인 워크플로</CardTitle><p className="text-xs text-muted-foreground">어댑터 세대에 연결되며 만료 시 자동 정리됩니다.</p></CardHeader><CardContent>{active.isLoading ? <LoadingState /> : active.error ? <ErrorState message={errorMessage(active.error)} retry={() => void active.refetch()} /> : active.data?.length ? <div className="divide-y divide-border">{active.data.map(item => <Link key={item.workflow_id} to="/workflows/$workflowId" params={{ workflowId: item.workflow_id }} className="flex min-w-0 flex-wrap items-center gap-3 py-3"><AdapterMark adapter={adapterById.get(item.adapter_id)} adapterId={item.adapter_id} size="sm" /><span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{adapterById.get(item.adapter_id)?.descriptor?.name ?? item.adapter_id} · 입력 항목 {item.challenge?.field_count ?? 0}개</span><span className="mt-1 block truncate text-[11px] text-muted-foreground">{item.resource ? resourceLabel(item.resource) : '리소스 확인 중'} · 수정 {formatDate(item.updated_at)}</span><span className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground"><Clock3 className="h-3 w-3" />만료 {formatDate(item.expires_at)}</span></span><StatusBadge state={item.in_progress ? 'running' : item.state} /></Link>)}</div> : <EmptyState title="대기 중인 워크플로가 없습니다." description="새 녹화에 추가 입력이 필요하면 이곳에 표시됩니다." />}</CardContent></Card>
      <Card><CardHeader><CardTitle>워크플로 기록</CardTitle><p className="text-xs text-muted-foreground">민감한 답변과 비밀 값은 기록에 포함되지 않습니다.</p></CardHeader><CardContent>{history.isLoading ? <LoadingState /> : history.error ? <ErrorState message={errorMessage(history.error)} retry={() => void history.refetch()} /> : history.data?.items.length ? <div className="max-h-[560px] divide-y divide-border overflow-auto">{history.data.items.map(event => <div key={event.id} className="flex min-w-0 items-start gap-3 py-3"><AdapterMark adapter={adapterById.get(event.adapter_id)} adapterId={event.adapter_id} size="xs" /><span className={`mt-1 h-2 w-2 shrink-0 rounded-full ${event.state === 'failed' || event.state === 'expired' ? 'bg-amber-500' : event.state === 'resolved' ? 'bg-emerald-500' : 'bg-primary'}`} /><div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{adapterById.get(event.adapter_id)?.descriptor?.name ?? event.adapter_id} · {workflowStateLabel(event.state)}</p><p className="mt-1 truncate text-[11px] text-muted-foreground">{event.resource ? resourceLabel(event.resource) : event.workflow_id}</p></div><time className="shrink-0 text-[10px] text-muted-foreground">{formatDate(event.at)}</time></div>)}</div> : <EmptyState title="기록된 워크플로가 없습니다." />}</CardContent></Card></div>
  </div>
}
