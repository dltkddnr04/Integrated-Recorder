import { useEffect, useState } from 'react'
import { Film } from 'lucide-react'
import { recordingsAPI } from '@/api'
import type { WatchView } from '@/types/api'

export function WatchPreview({ watch, className = '' }: { watch: WatchView; className?: string }) {
  const preview = watch.current_recording_preview
  const ordinal = preview?.image_archive_ordinal
  const src = watch.current_recording_id && preview?.available && ordinal != null
    ? recordingsAPI.previewFrame(watch.current_recording_id, ordinal)
    : undefined
  const [frames, setFrames] = useState<{ current?: string; previous?: string; pending?: string }>({})

  useEffect(() => {
    if (!src) {
      setFrames({})
      return
    }
    setFrames(current => current.current === src || current.pending === src ? current : { ...current, pending: src })
  }, [src])

  useEffect(() => {
    if (!frames.previous) return
    const previous = frames.previous
    const timer = window.setTimeout(() => setFrames(current => current.previous === previous ? { ...current, previous: undefined } : current), 360)
    return () => window.clearTimeout(timer)
  }, [frames.previous])

  const showImage = Boolean(frames.current)
  return <span role="img" aria-label={showImage ? '현재 방송 장면 미리보기' : '현재 방송 미리보기 대기 화면'} className={`relative grid aspect-video w-28 shrink-0 place-items-center overflow-hidden rounded-md border border-border bg-muted/40 ${className}`}>
    {!showImage && <Film className="h-5 w-5 text-muted-foreground" aria-hidden="true" />}
    {frames.previous && <img src={frames.previous} alt="" aria-hidden="true" className="live-preview-fade-out absolute inset-0 h-full w-full object-cover" />}
    {frames.current && <img src={frames.current} alt="" className={frames.previous ? 'live-preview-fade-in absolute inset-0 h-full w-full object-cover' : 'absolute inset-0 h-full w-full object-cover'} />}
    {frames.pending && <img
      key={frames.pending}
      src={frames.pending}
      alt=""
      aria-hidden="true"
      data-preview-pending="true"
      className="hidden"
      onLoad={event => {
        const loaded = event.currentTarget.getAttribute('src') ?? event.currentTarget.src
        setFrames(current => current.pending !== loaded ? current : current.current ? { current: loaded, previous: current.current } : { current: loaded })
      }}
      onError={event => {
        const failed = event.currentTarget.getAttribute('src') ?? event.currentTarget.src
        setFrames(current => current.pending === failed ? { ...current, pending: undefined } : current)
      }}
    />}
  </span>
}
