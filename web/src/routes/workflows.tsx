import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Clock3, ShieldQuestion, Workflow } from 'lucide-react'
import { workflowsQuery, qk } from '@/api/queries'
import { workflowsAPI } from '@/api'
import { PageHeading } from '@/components/page-heading'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import { formatDate, resourceLabel } from '@/lib/utils'

export function WorkflowsPage() {
  const active = useQuery(workflowsQuery); const history = useQuery({ queryKey: qk.workflowHistory, queryFn: workflowsAPI.history, staleTime: 15_000 })
  return <div className="page-enter"><PageHeading eyebrow="RESOLUTION ACTIVITY" title="워크플로" description="Adapter가 resource를 발견하고 필요한 정보를 요청하는 진행 중인 resolution입니다." actions={<Button variant="outline" onClick={() => { void active.refetch(); void history.refetch() }}>새로고침</Button>} />
    <div className="grid gap-4 xl:grid-cols-[1.1fr_.9fr]"><Card><CardHeader><CardTitle className="flex items-center gap-2"><Workflow className="h-4 w-4 text-primary" />활성 워크플로</CardTitle><p className="text-xs text-muted-foreground">Adapter generation에 묶이며 만료 시간 이후 자동 정리됩니다.</p></CardHeader><CardContent>{active.isLoading ? <LoadingState /> : active.error ? <ErrorState message={String(active.error)} retry={() => void active.refetch()} /> : active.data?.length ? <div className="divide-y divide-border">{active.data.map(item => <Link key={item.workflow_id} to="/workflows/$workflowId" params={{ workflowId: item.workflow_id }} className="flex flex-wrap items-center gap-3 py-3.5"><span className="grid h-9 w-9 place-items-center rounded-md bg-accent text-primary"><ShieldQuestion className="h-4 w-4" /></span><span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{item.adapter_id} · {item.challenge?.field_count ?? 0} fields</span><span className="mt-1 block truncate text-[11px] text-muted-foreground">{item.resource ? resourceLabel(item.resource) : 'Resource discovery 중'} · updated {formatDate(item.updated_at)}</span><span className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground"><Clock3 className="h-3 w-3" />만료 {formatDate(item.expires_at)}</span></span><StatusBadge state={item.in_progress ? 'running' : item.state} /></Link>)}</div> : <EmptyState title="대기 중인 워크플로가 없습니다." description="새 녹화가 추가 입력을 요구하면 이곳에 표시됩니다." />}</CardContent></Card>
      <Card><CardHeader><CardTitle>워크플로 기록</CardTitle><p className="text-xs text-muted-foreground">민감한 answer와 secret은 기록에 포함되지 않습니다.</p></CardHeader><CardContent>{history.isLoading ? <LoadingState /> : history.error ? <ErrorState message={String(history.error)} retry={() => void history.refetch()} /> : history.data?.items.length ? <div className="max-h-[560px] divide-y divide-border overflow-auto">{history.data.items.map(event => <div key={event.id} className="flex items-start gap-3 py-3"><span className={`mt-1 h-2 w-2 rounded-full ${event.state === 'failed' || event.state === 'expired' ? 'bg-amber-500' : event.state === 'resolved' ? 'bg-emerald-500' : 'bg-primary'}`} /><div className="min-w-0 flex-1"><p className="text-sm font-medium">{event.adapter_id} · {event.state.replaceAll('_', ' ')}</p><p className="mt-1 truncate text-[11px] text-muted-foreground">{event.resource ? resourceLabel(event.resource) : event.workflow_id}</p></div><time className="shrink-0 text-[10px] text-muted-foreground">{formatDate(event.at)}</time></div>)}</div> : <EmptyState title="기록된 워크플로가 없습니다." />}</CardContent></Card></div>
  </div>
}
