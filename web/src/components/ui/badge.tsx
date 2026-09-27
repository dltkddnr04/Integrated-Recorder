import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export function Badge({ className, tone = 'neutral', ...props }: HTMLAttributes<HTMLSpanElement> & { tone?: 'neutral' | 'blue' | 'green' | 'amber' | 'red' | 'purple' }) {
  return <span className={cn('inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-semibold leading-4', {
    'border-border bg-muted text-muted-foreground': tone === 'neutral', 'border-blue-200 bg-blue-50 text-blue-700 dark:border-blue-900 dark:bg-blue-950/70 dark:text-blue-300': tone === 'blue',
    'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/70 dark:text-emerald-300': tone === 'green',
    'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-900 dark:bg-amber-950/70 dark:text-amber-300': tone === 'amber',
    'border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/70 dark:text-red-300': tone === 'red',
    'border-violet-200 bg-violet-50 text-violet-700 dark:border-violet-900 dark:bg-violet-950/70 dark:text-violet-300': tone === 'purple',
  }, className)} {...props} />
}
