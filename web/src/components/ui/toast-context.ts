import { createContext } from 'react'

export type Toast = { id: number; title: string; description?: string; kind: 'success' | 'error' | 'info' }
export type ToastContextValue = { toast: (title: string, description?: string, kind?: Toast['kind']) => void }
export const ToastContext = createContext<ToastContextValue | null>(null)
