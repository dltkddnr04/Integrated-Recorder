import { useState, type ReactNode } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { ArrowLeft, ArrowRight, Database, Gauge, HardDrive, Layers3, RefreshCw, Server, ShieldCheck, Timer, TriangleAlert, Waves } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { storageMetricsQuery, storagePoolsQuery } from '@/api/queries'
import type { StorageMetricWindow } from '@/api'
import type { StoragePool } from '@/types/api'
import { MetricChart } from '@/components/storage/metric-chart'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import { PageHeading } from '@/components/page-heading'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectItem } from '@/components/ui/select'
import { errorMessage } from '@/lib/errors'
import { formatBytes } from '@/lib/utils'

export function StoragePage() {
  const pools = useQuery(storagePoolsQuery)
  return <div className="page-enter">
    <PageHeading eyebrow="운영 현황" title="저장소" description="저장 풀의 용량과 Integrated Recorder가 수행한 읽기·쓰기 및 수집 대기 상태를 확인합니다." actions={<Button variant="outline" onClick={() => void pools.refetch()}><RefreshCw className="h-4 w-4" />새로고침</Button>} />
    {pools.isLoading ? <LoadingState label="저장 풀을 불러오는 중입니다" /> : pools.error ? <ErrorState message={errorMessage(pools.error)} retry={() => void pools.refetch()} /> : <StoragePoolList pools={pools.data?.items ?? []} />}
  </div>
}

export function StoragePoolList({ pools }: { pools: StoragePool[] }) {
  if (!pools.length) return <EmptyState title="등록된 저장 풀이 없습니다." description="서버에서 사용할 수 있는 저장 풀이 확인되면 여기에 표시됩니다." />
  return <div className="grid min-w-0 gap-4 xl:grid-cols-2">{pools.map(pool => <StoragePoolCard key={pool.id} pool={pool} />)}</div>
}

export function StoragePoolCard({ pool }: { pool: StoragePool }) {
  const ratio = boundedRatio(pool.capacity.usage_ratio)
  return <Card className="min-w-0 overflow-hidden">
    <CardHeader className="flex-row items-start justify-between gap-4 border-b border-border/70">
      <div className="flex min-w-0 items-start gap-3"><span className="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary"><HardDrive className="h-5 w-5" /></span><div className="min-w-0"><Link to="/storage/$poolId" params={{ poolId: pool.id }} className="focus-ring rounded-sm"><CardTitle className="truncate text-base" title={pool.display_name}>{pool.display_name}</CardTitle></Link><p className="mt-1 truncate font-mono text-[11px] text-muted-foreground">{pool.id} · {kindLabel(pool.kind)} · {roleLabel(pool.role)}</p></div></div>
      <PoolHealth health={pool.health} />
    </CardHeader>
    <CardContent className="grid gap-5 pt-4 sm:grid-cols-2">
      <section aria-label="파일 시스템 용량" className="min-w-0"><div className="mb-2 flex items-baseline justify-between gap-2"><span className="text-xs font-medium text-muted-foreground">파일 시스템 용량</span><span className="text-xs tabular-nums">{formatBytes(pool.capacity.used_bytes)} / {formatBytes(pool.capacity.total_bytes)}</span></div><progress className="storage-progress" value={ratio} max={1} aria-label={`사용량 ${(ratio * 100).toFixed(0)}%`} /><div className="mt-2 flex justify-between text-[11px] text-muted-foreground"><span>{(ratio * 100).toFixed(1)}% 사용</span><span>{formatBytes(pool.capacity.available_bytes)} 여유</span></div></section>
      <div className="grid grid-cols-2 gap-3">
        <MetricValue icon={<Gauge className="h-3.5 w-3.5" />} label="Recorder 읽기" value={formatRate(pool.throughput.read_bytes_per_second)} />
        <MetricValue icon={<Waves className="h-3.5 w-3.5" />} label="Recorder 쓰기" value={formatRate(pool.throughput.write_bytes_per_second)} />
        <MetricValue icon={<Layers3 className="h-3.5 w-3.5" />} label="수집 버퍼" value={`${formatBytes(pool.buffer.used_bytes)} / ${formatBytes(pool.buffer.capacity_bytes)}`} />
        <MetricValue icon={<Database className="h-3.5 w-3.5" />} label="저장 대기열" value={`${pool.queue.objects}개 · ${formatBytes(pool.queue.bytes)}`} />
      </div>
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-border/70 pt-3 text-xs sm:col-span-2">
        <CeilingSummary pool={pool} />
        <span className="text-muted-foreground">가장 오래 대기 {formatDuration(pool.queue.oldest_age_seconds)} · 기록기 {pool.writers.active}/{pool.writers.limit}</span>
        <Link to="/storage/$poolId" params={{ poolId: pool.id }} className="ml-auto inline-flex items-center gap-1 font-medium text-primary">상세 보기 <ArrowRight className="h-3.5 w-3.5" /></Link>
      </div>
    </CardContent>
  </Card>
}

export function StoragePoolDetailPage() {
  const { poolId } = useParams({ from: '/storage/$poolId' })
  const [window, setWindow] = useState<StorageMetricWindow>('1h')
  const pools = useQuery(storagePoolsQuery)
  const pool = pools.data?.items.find(item => item.id === poolId)
  const metrics = useQuery(storageMetricsQuery(poolId, window, !pools.isLoading && !pools.error && Boolean(pool)))

  return <div className="page-enter">
    <PageHeading eyebrow="저장소 풀" title={pool?.display_name ?? poolId} description={pool ? `${pool.id} · ${kindLabel(pool.kind)} · ${roleLabel(pool.role)}` : '저장 풀 상태와 최근 측정값'} actions={<><Select aria-label="측정 기간" value={window} onValueChange={value => setWindow(value as StorageMetricWindow)} className="w-28"><SelectItem value="1h">최근 1시간</SelectItem><SelectItem value="6h">최근 6시간</SelectItem><SelectItem value="24h">최근 24시간</SelectItem></Select><Link to="/storage"><Button variant="outline"><ArrowLeft className="h-4 w-4" />저장소 목록</Button></Link></>} />
    {pools.isLoading ? <LoadingState label="저장 풀을 불러오는 중입니다" /> : pools.error ? <ErrorState message={errorMessage(pools.error)} retry={() => void pools.refetch()} /> : !pool ? <EmptyState title="저장 풀을 찾을 수 없습니다." description="저장소 목록에서 사용할 수 있는 풀을 확인하세요." /> : <>
      <PoolDetailSummary pool={pool} />
      <div className="mt-5 flex items-center justify-between gap-3"><h2 className="text-base font-semibold">최근 측정 추이</h2>{metrics.data && <span className="text-xs text-muted-foreground">{metrics.data.sample_interval_seconds}초 간격</span>}</div>
      <div className="mt-3 grid gap-4 xl:grid-cols-2">
        {metrics.isLoading ? <Card><CardContent className="pt-5"><LoadingState label="측정값을 불러오는 중입니다" /></CardContent></Card> : metrics.error ? <div className="xl:col-span-2"><ErrorState message={errorMessage(metrics.error)} retry={() => void metrics.refetch()} /></div> : metrics.data?.items.length ? <>
          <Card><CardContent className="pt-5"><MetricChart title="Recorder 읽기/쓰기" samples={metrics.data.items} kind="throughput" pool={pool} /></CardContent></Card>
          <Card><CardContent className="pt-5"><MetricChart title="수집 버퍼/저장 대기열" samples={metrics.data.items} kind="backlog" /></CardContent></Card>
        </> : <div className="xl:col-span-2"><EmptyState title="표시할 측정 기록이 없습니다." description="새 측정값이 수집되면 읽기·쓰기와 대기 상태의 추이가 표시됩니다." /></div>}
      </div>
    </>}
  </div>
}

function PoolDetailSummary({ pool }: { pool: StoragePool }) {
  const ratio = boundedRatio(pool.capacity.usage_ratio)
  return <div className="grid gap-4 xl:grid-cols-[1.1fr_1fr]">
    <Card><CardHeader className="flex-row items-center justify-between"><CardTitle className="flex items-center gap-2"><HardDrive className="h-4 w-4 text-primary" />풀 상태</CardTitle><PoolHealth health={pool.health} /></CardHeader><CardContent className="space-y-4"><div><div className="flex justify-between gap-3 text-xs"><span className="text-muted-foreground">파일 시스템 사용량</span><span className="tabular-nums">{formatBytes(pool.capacity.used_bytes)} / {formatBytes(pool.capacity.total_bytes)}</span></div><progress className="storage-progress mt-2" value={ratio} max={1} aria-label={`용량 사용량 ${(ratio * 100).toFixed(0)}%`} /><p className="mt-1 text-right text-[11px] text-muted-foreground">{formatBytes(pool.capacity.available_bytes)} 여유 · {(ratio * 100).toFixed(1)}% 사용</p></div><dl className="grid grid-cols-2 gap-4 border-t border-border pt-4 sm:grid-cols-4">{[['유형', pool.kind], ['역할', roleLabel(pool.role)], ['총 읽기', formatBytes(pool.throughput.read_bytes_total)], ['총 쓰기', formatBytes(pool.throughput.write_bytes_total)]].map(([label, value]) => <div key={label}><dt className="text-[11px] text-muted-foreground">{label}</dt><dd className="mt-1 break-words text-sm font-medium tabular-nums">{value}</dd></div>)}</dl></CardContent></Card>
    <Card><CardHeader><CardTitle className="flex items-center gap-2"><Server className="h-4 w-4 text-primary" />기록기와 대기 상태</CardTitle></CardHeader><CardContent className="grid grid-cols-2 gap-x-5 gap-y-4 sm:grid-cols-3">{[
      ['Recorder 읽기', formatRate(pool.throughput.read_bytes_per_second)], ['Recorder 쓰기', formatRate(pool.throughput.write_bytes_per_second)], ['읽기 대기 시간', `${pool.throughput.read_latency_ms.toFixed(1)} ms`], ['쓰기 대기 시간', `${pool.throughput.write_latency_ms.toFixed(1)} ms`],
      ['수집 버퍼', `${formatBytes(pool.buffer.used_bytes)} / ${formatBytes(pool.buffer.capacity_bytes)}`], ['저장 대기열', `${pool.queue.objects}개 · ${formatBytes(pool.queue.bytes)}`], ['가장 오래 대기', formatDuration(pool.queue.oldest_age_seconds)],
      ['활성 기록기', `${pool.writers.active} / ${pool.writers.limit}`], ['저장 오류 누계', pool.errors_total.toLocaleString('ko-KR')],
    ].map(([label, value]) => <div key={label} className="min-w-0"><p className="text-[11px] text-muted-foreground">{label}</p><p className="mt-1 truncate text-sm font-semibold tabular-nums" title={value}>{value}</p></div>)}<div className="col-span-2 border-t border-border pt-3 sm:col-span-3"><CeilingSummary pool={pool} /></div></CardContent></Card>
  </div>
}

function MetricValue({ icon, label, value }: { icon: ReactNode; label: string; value: string }) { return <div className="min-w-0"><div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">{icon}{label}</div><p className="mt-1 truncate text-xs font-semibold tabular-nums" title={value}>{value}</p></div> }
function PoolHealth({ health }: { health: string }) {
  const appearance = health === 'healthy' ? { label: '정상', tone: 'green' as const, icon: ShieldCheck } : health === 'degraded' ? { label: '저하', tone: 'amber' as const, icon: TriangleAlert } : health === 'unavailable' ? { label: '사용 불가', tone: 'red' as const, icon: TriangleAlert } : { label: health || '알 수 없음', tone: 'neutral' as const, icon: Timer }
  const Icon = appearance.icon
  return <Badge tone={appearance.tone}><Icon className="h-3 w-3" />{appearance.label}</Badge>
}
function CeilingSummary({ pool }: { pool: StoragePool }) {
  const ceiling = pool.estimated_ceiling
  const source = ceiling.source
  if (source === 'unknown' || (!ceiling.read_bytes_per_second && !ceiling.write_bytes_per_second)) return <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"><Gauge className="h-3.5 w-3.5" />예상 상한: 알 수 없음</span>
  const sourceLabel = source === 'observed' ? '관측 기반 예상 상한' : source === 'configured' ? '설정된 상한' : source === 'benchmarked' ? '측정된 상한' : '예상 상한'
  const parts = [ceiling.read_bytes_per_second ? `읽기 ${formatRate(ceiling.read_bytes_per_second)}` : '', ceiling.write_bytes_per_second ? `쓰기 ${formatRate(ceiling.write_bytes_per_second)}` : ''].filter(Boolean)
  return <span className="inline-flex flex-wrap items-center gap-x-1.5 text-xs"><Gauge className="h-3.5 w-3.5 text-muted-foreground" /><span className="text-muted-foreground">{sourceLabel}:</span><span className="font-medium tabular-nums">{parts.join(' · ')}</span></span>
}
function boundedRatio(value: number) { return Number.isFinite(value) ? Math.min(1, Math.max(0, value)) : 0 }
function formatRate(value: number) { return `${formatBytes(value)}/s` }
function formatDuration(seconds: number) { return Number.isFinite(seconds) && seconds >= 0 ? seconds < 60 ? `${seconds.toFixed(1)}초` : `${Math.floor(seconds / 60)}분 ${Math.floor(seconds % 60)}초` : '—' }
function roleLabel(role: string) { return role === 'primary' ? '주 저장 풀' : role }
function kindLabel(kind: string) { return kind === 'local' ? '로컬' : kind }
