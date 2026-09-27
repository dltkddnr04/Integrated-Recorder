import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

type Props = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'default' | 'secondary' | 'outline' | 'ghost' | 'destructive' | 'link'; size?: 'default' | 'sm' | 'lg' | 'icon' }
export const Button = forwardRef<HTMLButtonElement, Props>(function Button({ className, variant = 'default', size = 'default', ...props }, ref) {
  return <button ref={ref} className={cn('focus-ring inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors disabled:pointer-events-none disabled:opacity-50', {
    'bg-primary text-primary-foreground shadow-sm hover:bg-primary/90': variant === 'default',
    'bg-secondary text-secondary-foreground hover:bg-secondary/80': variant === 'secondary',
    'border border-input bg-background hover:bg-accent hover:text-accent-foreground': variant === 'outline',
    'hover:bg-accent hover:text-accent-foreground': variant === 'ghost',
    'bg-destructive text-white hover:bg-destructive/90': variant === 'destructive',
    'text-primary underline-offset-4 hover:underline': variant === 'link',
    'h-9 px-4 py-2': size === 'default', 'h-8 rounded px-3 text-xs': size === 'sm', 'h-11 rounded-md px-8': size === 'lg', 'h-9 w-9': size === 'icon',
  }, className)} {...props} />
})
