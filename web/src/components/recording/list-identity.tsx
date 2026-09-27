import type { RecordingListItem } from '@/types/api'
import { listResourceLabel } from '@/lib/recordings'

export function RecordingListIdentity({ item }: { item: RecordingListItem }) {
  return <div className="max-w-[190px]"><span className="block truncate text-xs font-medium">{item.adapter_name || item.adapter_id}</span><span className="mt-0.5 block truncate text-[10px] text-muted-foreground">{listResourceLabel(item)}</span></div>
}
