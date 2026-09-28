import type { RecordingListItem } from '@/types/api'
import { listResourceLabel } from '@/lib/recordings'
import { useQuery } from '@tanstack/react-query'
import { adaptersQuery } from '@/api/queries'
import { AdapterMark } from '@/components/adapter/adapter-mark'

export function RecordingListIdentity({ item }: { item: RecordingListItem }) {
  const adapters = useQuery(adaptersQuery)
  const adapter = adapters.data?.find(candidate => candidate.status.id === item.adapter_id)
  return <div className="flex max-w-[200px] min-w-0 items-center gap-2"><AdapterMark adapter={adapter} adapterId={item.adapter_id} name={item.adapter_name} size="sm" /><span className="min-w-0"><span className="block truncate text-xs font-medium" title={item.adapter_name || item.adapter_id}>{item.adapter_name || item.adapter_id}</span><span className="mt-0.5 block truncate text-[10px] text-muted-foreground" title={listResourceLabel(item)}>{listResourceLabel(item)}</span></span></div>
}
