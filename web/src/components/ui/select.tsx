import type { ReactNode, SelectHTMLAttributes } from 'react'
import { ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'

type SelectProps = Omit<SelectHTMLAttributes<HTMLSelectElement>, 'value' | 'onChange' | 'children'> & {
  value?: string
  onValueChange: (value: string) => void
  children: ReactNode
  placeholder?: string
}

/** Native select avoids inline styles injected by Radix under the strict app CSP. */
export function Select({ value, onValueChange, children, placeholder = '선택', disabled, id, className, 'aria-describedby': ariaDescribedBy, 'aria-invalid': ariaInvalid, ...props }: SelectProps) {
  return <span className="relative block min-w-0">
    <select {...props} id={id} value={value ?? ''} onChange={event => onValueChange(event.currentTarget.value)} disabled={disabled} aria-describedby={ariaDescribedBy} aria-invalid={ariaInvalid} className={cn('focus-ring h-9 w-full appearance-none rounded-md border border-input bg-background py-1.5 pl-3 pr-9 text-sm text-foreground disabled:cursor-not-allowed disabled:opacity-50', className)}>
      {value === undefined && <option value="">{placeholder}</option>}
      {children}
    </select>
    <ChevronDown aria-hidden="true" className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
  </span>
}

export function SelectItem({ value, children }: { value: string; children: ReactNode }) {
  return <option value={value}>{children}</option>
}
