import * as Alert from '@radix-ui/react-alert-dialog'
import type { ReactNode } from 'react'
import { Button } from './button'

export function Confirm({ trigger, title, description, confirmLabel = '확인', destructive = false, onConfirm, disabled }: { trigger: ReactNode; title: string; description: string; confirmLabel?: string; destructive?: boolean; onConfirm: () => void; disabled?: boolean }) {
  return <Alert.Root><Alert.Trigger asChild>{trigger}</Alert.Trigger><Alert.Portal><Alert.Overlay className="fixed inset-0 z-50 bg-black/45 backdrop-blur-[2px]" /><Alert.Content className="fixed left-1/2 top-1/2 z-50 w-[calc(100%-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 rounded-xl border border-border bg-card p-6 shadow-xl focus:outline-none"><Alert.Title className="pr-8 text-lg font-semibold">{title}</Alert.Title><Alert.Description className="mt-2 text-sm leading-6 text-muted-foreground">{description}</Alert.Description><div className="mt-6 flex justify-end gap-2"><Alert.Cancel asChild><Button variant="outline">취소</Button></Alert.Cancel><Alert.Action asChild><Button variant={destructive ? 'destructive' : 'default'} disabled={disabled} onClick={onConfirm}>{confirmLabel}</Button></Alert.Action></div></Alert.Content></Alert.Portal></Alert.Root>
}
