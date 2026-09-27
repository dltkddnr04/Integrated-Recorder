import { useEffect, useRef, useState } from 'react'
import type Hls from 'hls.js'
import { CircleAlert, Film } from 'lucide-react'

export function RecordingPlayer({ recordingId }: { recordingId: string }) {
  const videoRef = useRef<HTMLVideoElement>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    let hls: Hls | undefined
    let active = true
    setError('')
    const source = `/api/recordings/${encodeURIComponent(recordingId)}/play/master.m3u8`
    if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = source
    } else {
      void import('hls.js').then(({ default: HlsModule }) => {
        if (!active) return
        if (!HlsModule.isSupported()) { setError('이 브라우저에서 HLS 재생을 지원하지 않습니다.'); return }
        hls = new HlsModule({ enableWorker: true, lowLatencyMode: false })
        hls.loadSource(source)
        hls.attachMedia(video)
        hls.on(HlsModule.Events.ERROR, (_event, data) => { if (data.fatal && active) setError('재생을 시작하지 못했습니다. Archive 상태 또는 네트워크를 확인하세요.') })
      }).catch(() => { if (active) setError('HLS 재생 모듈을 불러오지 못했습니다.') })
    }
    return () => { active = false; hls?.destroy(); video.pause(); video.removeAttribute('src'); video.load() }
  }, [recordingId])
  return <div className="overflow-hidden rounded-lg border border-border bg-black"><div className="relative aspect-video"><video ref={videoRef} className="h-full w-full" controls playsInline preload="metadata" aria-label="녹화 VOD 재생" />{error && <div className="absolute inset-0 grid place-items-center bg-black/80 p-6 text-center text-white"><div><CircleAlert className="mx-auto h-6 w-6 text-amber-300" /><p className="mt-2 text-sm font-medium">재생할 수 없습니다</p><p className="mt-1 max-w-sm text-xs text-white/70">{error}</p></div></div>}{!error && <div className="pointer-events-none absolute left-3 top-3 inline-flex items-center gap-1.5 rounded bg-black/55 px-2 py-1 text-[10px] font-medium text-white"><Film className="h-3 w-3" />SOURCE SEGMENTS</div>}</div></div>
}
