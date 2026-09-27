import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import { CheckCircle2, CircleAlert, Info, X } from 'lucide-react'
import { Button } from './button'

type Toast = { id: number; title: string; description?: string; kind: 'success' | 'error' | 'info' }
type ToastContextValue = { toast: (title: string, description?: string, kind?: Toast['kind']) => void }
const Context = createContext<ToastContextValue | null>(null)
export function useToast() { const ctx = useContext(Context); if (!ctx) throw new Error('useToast requires ToastProvider'); return ctx }
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const toast = useCallback((title: string, description?: string, kind: Toast['kind'] = 'success') => {
    const id = Date.now() + Math.random()
    setItems(current => [...current.slice(-3), { id, title, description, kind }])
    window.setTimeout(() => setItems(current => current.filter(item => item.id !== id)), 5000)
  }, [])
  const value = useMemo(() => ({ toast }), [toast])
  return <Context.Provider value={value}>{children}<div className="fixed bottom-4 right-4 z-[100] flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2" role="region" aria-label="알림" aria-live="polite">{items.map(item => { const Icon = item.kind === 'error' ? CircleAlert : item.kind === 'info' ? Info : CheckCircle2; return <div key={item.id} className="flex gap-3 rounded-lg border border-border bg-card p-4 shadow-lg"><Icon className={`mt-0.5 h-4 w-4 shrink-0 ${item.kind === 'error' ? 'text-destructive' : 'text-primary'}`} /><div className="min-w-0 flex-1"><p className="text-sm font-semibold">{item.title}</p>{item.description && <p className="mt-1 text-xs text-muted-foreground">{item.description}</p>}</div><Button variant="ghost" size="icon" aria-label="알림 닫기" onClick={() => setItems(current => current.filter(x => x.id !== item.id))}><X className="h-4 w-4" /></Button></div> })}</div></Context.Provider>
}
