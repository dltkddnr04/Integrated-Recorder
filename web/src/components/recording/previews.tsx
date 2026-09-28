import { useState } from 'react'
import { Cable, Film } from 'lucide-react'
import { AdapterMark } from '@/components/adapter/adapter-mark'
import type { Adapter, PreviewFrame, PreviewSummary } from '@/types/api'
import { formatDuration } from '@/lib/utils'
import { formatPreviewClock, previewFrameURL, uniquePreviewFrames } from '@/lib/previews'

export function PreviewThumbnail({
  recordingId, summary, adapter, adapterId, adapterName, className = '',
}: {
  recordingId: string; summary?: PreviewSummary; adapter?: Adapter; adapterId?: string; adapterName?: string; className?: string
}) {
  const ordinal = summary?.mode === 'segment' && summary.frame_count > 0 ? summary.image_archive_ordinal : undefined
  const src = ordinal == null ? undefined : previewFrameURL(recordingId, ordinal, summary?.updated_at ?? String(ordinal))
  const [failedURL, setFailedURL] = useState<string>()
  const showFrame = Boolean(src && failedURL !== src)
  return <span role="img" className={`relative grid aspect-video shrink-0 place-items-center overflow-hidden rounded-md border border-border bg-muted ${className}`} aria-label={showFrame ? '장면 미리보기' : `${adapterName ?? adapterId ?? '녹화'} 대표 이미지`}>
    {showFrame && src ? <img key={src} src={src} alt="" loading="lazy" decoding="async" className="h-full w-full object-cover" onError={() => setFailedURL(src)} /> : <span className="grid h-full w-full place-items-center" aria-hidden="true">{adapter ? <AdapterMark adapter={adapter} adapterId={adapterId} name={adapterName} size="xs" /> : adapterId || adapterName ? <AdapterMark adapterId={adapterId} name={adapterName} size="xs" /> : <Cable className="h-4 w-4 text-muted-foreground" />}</span>}
  </span>
}

export function PreviewFrameGrid({ items, recordingId, onSeek, label = '장면 미리보기' }: {
  items: PreviewFrame[]; recordingId: string; onSeek: (seconds: number) => void; label?: string
}) {
  const frames = uniquePreviewFrames(items)
  if (!frames.length) return <p className="text-xs text-muted-foreground">표시할 장면 미리보기가 없습니다.</p>
  return <div role="group" className="grid grid-cols-3 gap-2 sm:grid-cols-4 md:grid-cols-6 xl:grid-cols-8" aria-label={label}>
    {frames.map(frame => {
      const time = formatPreviewClock(frame.frame_time_seconds)
      return <PreviewFrameCell key={frame.archive_ordinal} frame={frame} recordingId={recordingId} time={time} onSeek={onSeek} />
    })}
  </div>
}

function PreviewFrameCell({ frame, recordingId, time, onSeek }: { frame: PreviewFrame; recordingId: string; time: string; onSeek: (seconds: number) => void }) {
  const src = previewFrameURL(recordingId, frame.archive_ordinal, frame.generated_at)
  const [failedURL, setFailedURL] = useState<string>()
  return <button type="button" onClick={() => onSeek(frame.frame_time_seconds)} aria-label={`${formatDuration(frame.frame_time_seconds)} 위치로 이동`} title={`${time} 위치로 이동`} className="focus-ring group relative aspect-video min-w-0 overflow-hidden rounded-md border border-border bg-muted text-left transition hover:border-primary hover:ring-1 hover:ring-primary/40">
        {failedURL === src ? <Film aria-hidden="true" className="absolute inset-0 m-auto h-5 w-5 text-muted-foreground" /> : <img src={src} alt="" loading="lazy" decoding="async" className="h-full w-full object-cover transition group-hover:brightness-90" onError={() => setFailedURL(src)} />}
        <span aria-hidden="true" className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/80 to-transparent px-1.5 pb-1 pt-3 text-right font-mono text-[9px] leading-none text-white">{time}</span>
  </button>
}

export function PreviewStateMessage({ state }: { state?: PreviewSummary['state'] }) {
  const text: Record<NonNullable<PreviewSummary['state']>, string> = {
    disabled: '장면 미리보기를 사용하지 않습니다.',
    unavailable: '이 서버에서는 미리보기 생성 기능을 사용할 수 없습니다.',
    queued: '장면 미리보기를 대기 중입니다.',
    processing: '장면 미리보기를 생성하고 있습니다.',
    partial: '일부 장면 미리보기를 생성했습니다.',
    ready: '장면 미리보기를 사용할 수 있습니다.',
    failed: '장면 미리보기를 생성하지 못했습니다.',
  }
  return <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground"><Film className="h-3.5 w-3.5 shrink-0" />{state ? text[state] : '미리보기 상태를 확인할 수 없습니다.'}</p>
}
