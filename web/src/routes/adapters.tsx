import { Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowRight, CircleAlert, Fingerprint, Radio, RefreshCw } from 'lucide-react'
import { adaptersAPI } from '@/api'
import { adaptersQuery } from '@/api/queries'
import { PageHeading } from '@/components/page-heading'
import { errorMessage } from '@/lib/errors'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import type { Adapter } from '@/types/api'
import { AdapterMark } from '@/components/adapter/adapter-mark'
import { useToast } from '@/components/ui/use-toast'

export function AdaptersPage() {
  const query = useQuery(adaptersQuery)
  const client = useQueryClient()
  const { toast } = useToast()
  const reconcile = useMutation({
    mutationFn: adaptersAPI.reconcile,
    onSuccess: async result => {
      await client.invalidateQueries({ queryKey: ['adapters'] })
      if (result.state === 'rejected') toast('어댑터 검색을 마쳤습니다.', `유효하지 않은 후보 ${result.rejected_count}개를 건너뛰었습니다.`, 'info')
      else if (result.state === 'failed') toast('어댑터 검색을 완료하지 못했습니다.', '현재 활성 어댑터는 유지됩니다. 잠시 후 다시 시도해 주세요.', 'error')
      else if (result.rejected_count > 0) toast('유효한 어댑터 세대를 활성화했습니다.', `유효하지 않은 후보 ${result.rejected_count}개를 건너뛰었습니다.`, 'info')
      else toast(result.state === 'activated' ? '새 어댑터 세대를 활성화했습니다.' : '어댑터가 최신 상태입니다.')
    },
    onError: error => toast('어댑터 검색 실패', errorMessage(error), 'error'),
  })
  const actions = <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={reconcile.isPending} onClick={() => reconcile.mutate()}><RefreshCw className={`h-4 w-4 ${reconcile.isPending ? 'animate-spin' : ''}`} />어댑터 다시 검색</Button><Button variant="ghost" onClick={() => void query.refetch()}><RefreshCw className="h-4 w-4" />새로고침</Button></div>
  return <div className="page-enter"><PageHeading eyebrow="외부 연결" title="어댑터" description="플랫폼별 원본 탐색은 별도 어댑터 프로세스가 담당합니다. 새 실행 파일은 자동으로 확인되며, 기존 녹화는 시작 당시 어댑터 세대를 계속 사용합니다." actions={actions} />{query.isLoading ? <LoadingState /> : query.error ? <ErrorState message={errorMessage(query.error)} retry={() => void query.refetch()} /> : query.data?.length ? <div className="overflow-hidden rounded-lg border border-border bg-card"><div className="grid grid-cols-[minmax(180px,1.5fr)_minmax(110px,.7fr)_minmax(120px,.8fr)_minmax(130px,.8fr)_80px] gap-3 border-b border-border bg-muted/50 px-4 py-2.5 text-[10px] font-semibold text-muted-foreground max-md:hidden"><span>어댑터</span><span>상태</span><span>프로토콜</span><span>기능</span><span className="text-right">세대</span></div><div className="divide-y divide-border">{query.data.map(adapter => <AdapterRow key={adapter.status.id} adapter={adapter} />)}</div></div> : <EmptyState title="설치된 어댑터가 없습니다." description="관리자가 실행 가능한 어댑터 파일을 어댑터 디렉터리에 넣으면 Runtime Host가 자동으로 검증하고 활성화합니다. 다시 검색을 눌러 즉시 확인할 수도 있습니다." />}</div>
}
function AdapterRow({ adapter }: { adapter: Adapter }) { const descriptor = adapter.descriptor; const caps = descriptor?.capabilities ?? []; return <Link to="/adapters/$adapterId" params={{ adapterId: adapter.status.id }} search={{ tab: undefined }} className="grid min-w-0 grid-cols-[minmax(180px,1.5fr)_minmax(110px,.7fr)_minmax(120px,.8fr)_minmax(130px,.8fr)_80px] items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/30 max-md:grid-cols-[1fr_auto] max-md:gap-y-2"><span className="flex min-w-0 items-center gap-3"><AdapterMark adapter={adapter} size="md" /><span className="min-w-0"><span className="block truncate text-sm font-semibold" title={descriptor?.name ?? adapter.status.name ?? adapter.status.id}>{descriptor?.name ?? adapter.status.name ?? adapter.status.id}</span><span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">{adapter.status.id} · {descriptor?.version ?? adapter.status.version ?? '—'}</span></span></span><span><StatusBadge state={adapter.status.state} />{adapter.status.error && <span className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground"><CircleAlert className="h-3 w-3" />상세 상태 참조</span>}</span><span className="text-xs text-muted-foreground">v{descriptor?.protocol_version ?? adapter.status.protocol_version ?? '—'}</span><span className="flex flex-wrap gap-1">{caps.length ? caps.slice(0, 2).map(cap => <Badge key={cap} tone="blue">{cap}</Badge>) : <span className="text-xs text-muted-foreground">기본 기능</span>}</span><span className="text-right text-xs tabular-nums text-muted-foreground">{adapter.status.generation ?? '—'}<ArrowRight className="ml-2 inline h-3.5 w-3.5" /></span><span className="col-span-2 flex gap-2 text-[11px] text-muted-foreground md:hidden"><Radio className="h-3 w-3" />{descriptor?.media_types?.join(', ') || '미디어 형식 미보고'}<span>·</span><Fingerprint className="h-3 w-3" />재시작 {adapter.status.restart_attempts ?? 0}회</span></Link> }
