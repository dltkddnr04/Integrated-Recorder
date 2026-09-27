import { useContext } from 'react'
import { ToastContext } from './toast-context'

export function useToast() {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast requires ToastProvider')
  return ctx
}
