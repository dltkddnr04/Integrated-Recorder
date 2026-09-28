import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Radio } from 'lucide-react'
import { adaptersAPI, watchesAPI, type WatchMutationBody } from '@/api'
import { adaptersQuery, dashboardQuery, qk } from '@/api/queries'
import { AdapterMark } from '@/components/adapter/adapter-mark'
import { PageHeading } from '@/components/page-heading'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import { WatchForm } from '@/components/watch/watch-form'
import { watchCapableAdapters } from '@/lib/watches'
import { errorMessage } from '@/lib/errors'
import { useToast } from '@/components/ui/use-toast'

export function WatchNewPage() {
  const [adapterId, setAdapterId] = useState('')
  const adapters = useQuery(adaptersQuery)
  const client = useQueryClient()
  const navigate = useNavigate()
  const { toast } = useToast()
  const pendingBody = useRef<WatchMutationBody | undefined>(undefined)
  const selected = adapters.data?.find(adapter => adapter.status.id === adapterId)
  const schema = useQuery({
    queryKey: qk.schema(adapterId),
    queryFn: () => adaptersAPI.schema(adapterId),
    enabled: Boolean(selected?.descriptor), retry: false, staleTime: 60_000,
  })
  const create = useMutation({
    mutationFn: () => {
      const body = pendingBody.current
      pendingBody.current = undefined
      if (!body) throw new Error('Watch 입력을 사용할 수 없습니다.')
      return watchesAPI.create(body)
    },
    gcTime: 0,
    onSuccess: async watch => {
      toast('자동 녹화를 등록했습니다.')
      await Promise.all([
        client.invalidateQueries({ queryKey: qk.watches }),
        client.invalidateQueries({ queryKey: dashboardQuery.queryKey }),
      ])
      await navigate({ to: '/watches/$watchId', params: { watchId: watch.id } })
    },
    onError: () => toast('자동 녹화를 등록하지 못했습니다.', '입력 내용을 확인한 뒤 다시 시도해 주세요.', 'error'),
  })
  const eligible = watchCapableAdapters(adapters.data)

  return <div className="page-enter">
    <PageHeading eyebrow="무인 녹화" title="Watch 등록" description="한 번 등록하면 서버가 방송 시작을 확인하고 이후 회차를 자동 녹화합니다." actions={<Link to="/watches"><Button variant="outline"><ArrowLeft className="h-4 w-4" />자동 녹화 목록</Button></Link>} />
    {!adapterId && <section><h2 className="mb-1 text-base font-semibold">어댑터 선택</h2><p className="mb-4 text-sm text-muted-foreground">자동 방송 감지 기능을 제공하는 준비된 어댑터만 표시합니다.</p>{adapters.isLoading ? <LoadingState /> : adapters.error ? <ErrorState message={errorMessage(adapters.error)} retry={() => void adapters.refetch()} /> : eligible.length ? <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{eligible.map(adapter => <button key={adapter.status.id} type="button" onClick={() => setAdapterId(adapter.status.id)} className="focus-ring rounded-lg border border-border bg-card p-4 text-left transition hover:border-primary/50 hover:shadow-sm"><div className="flex items-center gap-3"><AdapterMark adapter={adapter} size="md" /><span className="min-w-0 flex-1"><span className="block truncate font-semibold" title={adapter.descriptor?.name}>{adapter.descriptor?.name}</span><span className="mt-0.5 block truncate text-xs text-muted-foreground">{adapter.status.id} · v{adapter.descriptor?.version}</span></span><StatusBadge state={adapter.status.state} /></div><div className="mt-3 flex items-center gap-2 text-xs text-muted-foreground"><Radio className="h-3.5 w-3.5 text-primary" />방송 자동 감지 지원</div></button>)}</div> : <EmptyState title="자동 감지를 지원하는 어댑터가 없습니다." description="어댑터가 watch 기능을 선언하고 준비 상태가 되면 여기에서 Watch를 등록할 수 있습니다." />}</section>}
    {selected && <div className="mt-4"><Card className="mb-4"><CardContent className="flex flex-wrap items-center gap-3 py-3"><AdapterMark adapter={selected} adapterId={selected.status.id} showName size="sm" /><StatusBadge state={selected.status.state} /><Button type="button" variant="ghost" size="sm" className="ml-auto" onClick={() => setAdapterId('')} disabled={create.isPending}>어댑터 변경</Button></CardContent></Card>{schema.isLoading ? <LoadingState label="어댑터 입력 양식을 불러오는 중입니다" /> : schema.error || !schema.data ? <ErrorState message={errorMessage(schema.error ?? '어댑터 입력 양식을 사용할 수 없습니다.')} retry={() => void schema.refetch()} /> : <WatchForm key={selected.status.id} adapter={selected} schema={schema.data.input_schema} submitLabel={create.isPending ? '등록 중…' : '자동 녹화 등록'} busy={create.isPending} onSubmit={value => { pendingBody.current = value; create.mutate() }} />}{create.isPending && <p role="status" className="mt-3 text-sm text-muted-foreground">Watch를 저장하고 있습니다.</p>}</div>}
  </div>
}
