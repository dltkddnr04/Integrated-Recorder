import { useEffect, useRef, useState } from 'react'
import { getCoreRowModel, flexRender, useReactTable, type ColumnDef } from '@tanstack/react-table'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronRight, Search, SlidersHorizontal, Trash2, X } from 'lucide-react'
import { recordingsAPI } from '@/api'
import { adaptersQuery, qk } from '@/api/queries'
import { formatBytes, formatDate, formatDuration } from '@/lib/utils'
import { localDateBoundary, localDateInputValue } from '@/lib/recordings'
import type { RecordingQuery } from '@/api'
import { recordingStates, type RecordingListItem } from '@/types/api'
import { PageHeading } from '@/components/page-heading'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Confirm } from '@/components/ui/confirm'
import { Input } from '@/components/ui/input'
import { Select, SelectItem } from '@/components/ui/select'
import { Card } from '@/components/ui/card'
import { ErrorState, LoadingState, EmptyState } from '@/components/query-state'
import { useToast } from '@/components/ui/use-toast'
import { RecordingListIdentity } from '@/components/recording/list-identity'
import { AdapterMark } from '@/components/adapter/adapter-mark'
import { errorMessage } from '@/lib/errors'
import { useDebouncedValue } from '@/hooks/use-debounced-value'
import { integrityStatusLabel, recordingStateLabel } from '@/lib/labels'

type SearchState = { q?: string; state?: string; adapter?: string; resource_type?: string; started_after?: string; started_before?: string; has_gaps?: string; integrity?: string; tag?: string; sort?: string; limit?: number; cursor?: string }
const columns: ColumnDef<RecordingListItem>[] = [
  { accessorKey: 'state', header: '상태', cell: ({ row }) => <StatusBadge state={row.original.state} /> },
  { accessorKey: 'title', header: '제목 / ID', cell: ({ row }) => <div className="max-w-[230px]"><Link className="block truncate font-medium hover:text-primary" to="/recordings/$recordingId" params={{ recordingId: row.original.id }}>{row.original.title || '제목 없는 녹화'}</Link><span className="mt-0.5 block truncate font-mono text-[10px] text-muted-foreground">{row.original.id}</span></div> },
  { accessorKey: 'adapter_id', header: '어댑터 / 리소스', cell: ({ row }) => <RecordingListIdentity item={row.original} /> },
  { accessorKey: 'started_at', header: '시작', cell: ({ row }) => <span className="whitespace-nowrap text-xs text-muted-foreground">{formatDate(row.original.started_at)}</span> },
  { accessorKey: 'duration_seconds', header: '시간', cell: ({ row }) => <span className="tabular-nums">{formatDuration(row.original.duration_seconds)}</span> },
  { accessorKey: 'archive_size_bytes', header: '보관 용량', cell: ({ row }) => <span className="tabular-nums">{formatBytes(row.original.archive_size_bytes)}</span> },
  { accessorKey: 'segment_count', header: '세그먼트', cell: ({ row }) => <span className="tabular-nums">{row.original.segment_count ?? 0}</span> },
  { accessorKey: 'gap_count', header: '누락', cell: ({ row }) => row.original.gap_count ? <Badge tone="amber">{row.original.gap_count}</Badge> : <span className="text-muted-foreground">0</span> },
  { accessorKey: 'integrity', header: '무결성', cell: ({ row }) => <StatusBadge state={row.original.integrity} /> },
  { accessorKey: 'tags', header: '태그', cell: ({ row }) => <div className="flex max-w-32 flex-wrap gap-1">{row.original.tags?.slice(0, 2).map(tag => <Badge key={tag}>{tag}</Badge>)}</div> },
]

export function RecordingsPage() {
  const search = useSearch({ from: '/recordings' }) as SearchState
  const navigate = useNavigate({ from: '/recordings' }); const client = useQueryClient(); const { toast } = useToast()
  const [searchText, setSearchText] = useState(search.q ?? '')
  const debouncedSearchText = useDebouncedValue(searchText, 300)
  const routeSearchValue = search.q ?? ''
  const lastRouteSearch = useRef(routeSearchValue)
  const routeSyncPending = useRef(false)
  const [filterOpen, setFilterOpen] = useState(false)
  useEffect(() => {
    if (lastRouteSearch.current === routeSearchValue) return
    lastRouteSearch.current = routeSearchValue
    routeSyncPending.current = true
    setSearchText(routeSearchValue)
  }, [routeSearchValue])
  useEffect(() => {
    if (routeSyncPending.current) {
      if (debouncedSearchText === routeSearchValue) routeSyncPending.current = false
      return
    }
    if (debouncedSearchText !== searchText) return
    if (debouncedSearchText === routeSearchValue) return
    lastRouteSearch.current = debouncedSearchText
    void navigate({ search: previous => ({ ...previous, q: debouncedSearchText || undefined, cursor: undefined }) })
  }, [debouncedSearchText, routeSearchValue, searchText, navigate])
  const query: RecordingQuery = { ...search }
  const records = useQuery({ queryKey: qk.recordings(query), queryFn: () => recordingsAPI.list(query), staleTime: 5000, refetchInterval: queryState => search.state === 'recording' || queryState.state.data?.items.some(item => item.state === 'recording') ? 5000 : 25_000, refetchIntervalInBackground: false })
  const adapters = useQuery(adaptersQuery)
  const remove = useMutation({ mutationFn: recordingsAPI.remove, onSuccess: (_, id) => { toast('보관 데이터를 삭제했습니다.'); void client.invalidateQueries({ queryKey: ['recordings'] }); void client.invalidateQueries({ queryKey: qk.dashboard }); void client.removeQueries({ queryKey: qk.recording(id) }) }, onError: error => toast('삭제 실패', errorMessage(error), 'error') })
  const set = (patch: Partial<SearchState>) => { void navigate({ search: previous => ({ ...previous, ...patch, cursor: undefined }) }) }
  const clearFilters = () => { setSearchText(''); void navigate({ search: { sort: '-started_at', limit: 25 } }) }
  const table = useReactTable({ data: records.data?.items ?? [], columns, getCoreRowModel: getCoreRowModel(), manualPagination: true, pageCount: -1 })
  const activeFilterCount = Object.entries(search).filter(([key, value]) => !['cursor', 'limit', 'sort'].includes(key) && Boolean(value)).length
  const goNext = () => { if (!records.data?.next_cursor) return; void navigate({ search: previous => ({ ...previous, cursor: records.data?.next_cursor }) }) }
  return <div className="page-enter"><PageHeading eyebrow="원본 보관함" title="녹화" description="원본 세그먼트와 보존 상태를 검색하고 관리합니다." actions={<><Badge tone="neutral">{records.data?.total ?? '—'}개 녹화</Badge><Link to="/new"><Button><span className="text-lg leading-none">+</span>새 녹화</Button></Link></>} />
    <Card className="mb-4 p-2.5"><div className="flex flex-col gap-2.5 xl:flex-row xl:flex-nowrap xl:items-center"><div className="relative min-w-0 flex-1 xl:min-w-[180px]"><Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" /><Input value={searchText} onChange={event => { routeSyncPending.current = false; setSearchText(event.target.value) }} placeholder="제목, 어댑터, 리소스, 태그 검색" aria-label="녹화 검색" className="pl-9" /></div><div className="flex min-w-0 flex-wrap items-center gap-2 xl:flex-nowrap"><div className="w-full sm:w-[126px]"><Select value={search.state ?? '__all'} onValueChange={value => set({ state: value === '__all' ? undefined : value })} aria-label="상태 필터"><SelectItem value="__all">모든 상태</SelectItem>{recordingStates.map(value => <SelectItem key={value} value={value}>{recordingStateLabel(value)}</SelectItem>)}</Select></div><div className="w-full sm:w-[150px]"><Select value={search.adapter ?? '__all'} onValueChange={value => set({ adapter: value === '__all' ? undefined : value })} aria-label="어댑터 필터"><SelectItem value="__all">모든 어댑터</SelectItem>{adapters.data?.filter(item => item.descriptor).map(item => <SelectItem key={item.status.id} value={item.status.id}>{item.descriptor?.name ?? item.status.id}</SelectItem>)}</Select></div><div className="w-full sm:w-[120px]"><Select value={search.has_gaps ?? '__all'} onValueChange={value => set({ has_gaps: value === '__all' ? undefined : value })} aria-label="누락 필터"><SelectItem value="__all">누락 여부</SelectItem><SelectItem value="true">누락 있음</SelectItem><SelectItem value="false">누락 없음</SelectItem></Select></div><Button className="shrink-0" variant={filterOpen ? 'secondary' : 'outline'} size="sm" onClick={() => setFilterOpen(!filterOpen)}><SlidersHorizontal className="h-4 w-4" />추가 필터{activeFilterCount > 0 && <Badge tone="blue">{activeFilterCount}</Badge>}</Button><div className="w-full sm:w-[140px]"><Select value={search.sort ?? '-started_at'} onValueChange={value => set({ sort: value })} aria-label="정렬 기준"><SelectItem value="-started_at">최근 시작</SelectItem><SelectItem value="started_at">오래된 시작</SelectItem><SelectItem value="-created_at">최근 추가</SelectItem><SelectItem value="created_at">먼저 추가</SelectItem><SelectItem value="-duration">긴 시간순</SelectItem><SelectItem value="duration">짧은 시간순</SelectItem><SelectItem value="-size">큰 용량순</SelectItem><SelectItem value="size">작은 용량순</SelectItem></Select></div></div></div>
      {filterOpen && <div className="mt-2 grid gap-2 border-t border-border pt-3 sm:grid-cols-2 xl:grid-cols-6"><label className="space-y-1 text-xs font-medium text-muted-foreground">무결성<select aria-label="무결성 필터" className="focus-ring h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground" value={search.integrity ?? '__all'} onChange={event => set({ integrity: event.target.value === '__all' ? undefined : event.target.value })}><option value="__all">모든 상태</option>{['unknown', 'verifying', 'verified', 'degraded', 'failed'].map(value => <option key={value} value={value}>{integrityStatusLabel(value)}</option>)}</select></label><label className="space-y-1 text-xs font-medium text-muted-foreground">태그<Input value={search.tag ?? ''} onChange={event => set({ tag: event.target.value || undefined })} placeholder="태그" /></label><label className="space-y-1 text-xs font-medium text-muted-foreground">리소스 유형<Input value={search.resource_type ?? ''} onChange={event => set({ resource_type: event.target.value || undefined })} placeholder="유형" /></label><label className="space-y-1 text-xs font-medium text-muted-foreground">시작 이후 · 현지 날짜<Input type="date" value={localDateInputValue(search.started_after)} onChange={event => set({ started_after: localDateBoundary(event.target.value) })} /></label><label className="space-y-1 text-xs font-medium text-muted-foreground">시작 이전 · 현지 날짜<Input type="date" value={localDateInputValue(search.started_before)} onChange={event => set({ started_before: localDateBoundary(event.target.value, true) })} /></label><div className="flex items-end"><Button variant="ghost" size="sm" onClick={clearFilters}><X className="h-3.5 w-3.5" />필터 초기화</Button></div></div>}
    </Card>
    {records.isLoading ? <LoadingState /> : records.error ? <ErrorState message={errorMessage(records.error)} retry={() => void records.refetch()} /> : !records.data?.items.length ? <EmptyState title="조건에 맞는 녹화가 없습니다." description="검색어와 필터를 조정하거나 새 녹화를 시작하세요." /> : <>
      <div className="hidden overflow-hidden rounded-lg border border-border bg-card lg:block"><div className="overflow-x-auto"><table className="w-full min-w-[1180px] text-left text-xs"><thead className="bg-muted/60 text-[10px] uppercase tracking-wide text-muted-foreground"><tr>{table.getHeaderGroups()[0]?.headers.map(header => <th key={header.id} className="px-3 py-3 font-semibold">{flexRender(header.column.columnDef.header, header.getContext())}</th>)}<th className="px-3 py-3 text-right font-semibold">관리</th></tr></thead><tbody>{table.getRowModel().rows.map(row => <tr key={row.id} className="border-t border-border/70 transition-colors hover:bg-muted/30">{row.getVisibleCells().map(cell => <td key={cell.id} className="px-3 py-3">{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}<td className="px-3 py-3 text-right"><Confirm trigger={<Button variant="ghost" size="icon" aria-label={`${row.original.title || row.original.id} 삭제`} disabled={remove.isPending || row.original.state === 'recording'}><Trash2 className="h-4 w-4 text-muted-foreground hover:text-destructive" /></Button>} title="녹화 보관 데이터를 삭제할까요?" description="이 작업은 원본 녹화 보관 데이터와 모든 페이로드를 삭제합니다. 되돌릴 수 없습니다." confirmLabel="보관 데이터 삭제" destructive onConfirm={() => remove.mutate(row.original.id)} disabled={remove.isPending || row.original.state === 'recording'} /></td></tr>)}</tbody></table></div></div>
      <div className="space-y-2 lg:hidden">{records.data.items.map(item => <MobileRecording key={item.id} item={item} onDelete={() => remove.mutate(item.id)} busy={remove.isPending} />)}</div>
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground"><span>{records.data.total}개 중 현재 페이지 {records.data.items.length}개</span><div className="flex gap-2"><Button variant="outline" size="sm" onClick={goNext} disabled={!records.data.next_cursor}><span>다음</span><ChevronRight className="h-4 w-4" /></Button></div></div>
    </>}
  </div>
}
function MobileRecording({ item, onDelete, busy }: { item: RecordingListItem; onDelete: () => void; busy: boolean }) { const adapters = useQuery(adaptersQuery); const adapter = adapters.data?.find(candidate => candidate.status.id === item.adapter_id); return <Card className="p-3.5"><div className="flex items-start justify-between gap-2"><div className="min-w-0"><Link to="/recordings/$recordingId" params={{ recordingId: item.id }} className="block truncate text-sm font-semibold hover:text-primary" title={item.title}>{item.title || '제목 없는 녹화'}</Link><div className="mt-2 flex min-w-0 items-center gap-2"><AdapterMark adapter={adapter} adapterId={item.adapter_id} name={item.adapter_name} size="xs" /><span className="min-w-0 truncate text-[11px] text-muted-foreground">{item.adapter_name || item.adapter_id} · {formatDate(item.started_at)}</span></div><p className="mt-1 truncate text-[11px] text-muted-foreground">{item.resource_type && item.resource_id ? `${item.resource_type} / ${item.resource_id}` : '—'}</p></div><StatusBadge state={item.state} /></div><div className="mt-3 grid grid-cols-3 gap-2 border-t border-border pt-3 text-xs"><span>{formatDuration(item.duration_seconds)}</span><span>{formatBytes(item.archive_size_bytes)}</span><span>{item.segment_count} 세그먼트</span></div><div className="mt-3 flex items-center justify-between"><div className="flex gap-1">{item.gap_count ? <Badge tone="amber">{item.gap_count}개 누락</Badge> : <Badge>누락 없음</Badge>}<StatusBadge state={item.integrity} /></div><Confirm trigger={<Button variant="ghost" size="sm" aria-label="삭제" disabled={busy || item.state === 'recording'}><Trash2 className="h-4 w-4 text-destructive" /></Button>} title="보관 데이터를 삭제할까요?" description="이 작업은 보관 데이터와 모든 페이로드를 삭제합니다." confirmLabel="보관 데이터 삭제" destructive onConfirm={onDelete} disabled={busy || item.state === 'recording'} /></div></Card> }
