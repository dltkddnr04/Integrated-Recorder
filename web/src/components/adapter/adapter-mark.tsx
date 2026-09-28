import { useState } from 'react'
import { Cable } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { Adapter } from '@/types/api'

const sizes = {
  xs: { box: 'h-6 w-6 rounded', image: 'h-4 w-4', fallback: 'h-3.5 w-3.5' },
  sm: { box: 'h-8 w-8 rounded-md', image: 'h-5 w-5', fallback: 'h-4 w-4' },
  md: { box: 'h-10 w-10 rounded-lg', image: 'h-7 w-7', fallback: 'h-5 w-5' },
  lg: { box: 'h-12 w-12 rounded-xl', image: 'h-9 w-9', fallback: 'h-6 w-6' },
} as const

type AdapterMarkProps = {
  adapter?: Adapter
  adapterId?: string
  name?: string
  size?: keyof typeof sizes
  showName?: boolean
  className?: string
  nameClassName?: string
}

export function AdapterMark({ adapter, adapterId, name, size = 'sm', showName = false, className, nameClassName }: AdapterMarkProps) {
  const [failedURL, setFailedURL] = useState<string>()
  const descriptor = adapter?.descriptor
  const iconURL = descriptor?.branding?.icon_url
  const displayName = name ?? descriptor?.name ?? adapter?.status.name ?? adapterId ?? adapter?.status.id ?? '어댑터'
  const dimensions = sizes[size]
  const showIcon = Boolean(iconURL && iconURL !== failedURL)
  return <span className={cn('flex min-w-0 items-center gap-2', className)} title={displayName} aria-label={displayName}>
    <span className={cn('grid shrink-0 aspect-square place-items-center overflow-hidden bg-muted', dimensions.box)} aria-hidden="true">
      {showIcon ? <img key={iconURL} src={iconURL} alt="" className={cn('max-h-full max-w-full shrink-0 object-contain', dimensions.image)} onError={() => setFailedURL(iconURL)} /> : <Cable className={cn('shrink-0 text-muted-foreground', dimensions.fallback)} />}
    </span>
    {showName && <span className={cn('min-w-0 truncate text-sm font-medium', nameClassName)}>{displayName}</span>}
  </span>
}
