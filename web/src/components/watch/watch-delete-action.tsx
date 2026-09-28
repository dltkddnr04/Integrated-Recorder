import { Trash2 } from 'lucide-react'
import { Confirm } from '@/components/ui/confirm'
import { Button } from '@/components/ui/button'

export function WatchDeleteAction({ onDelete, disabled }: { onDelete: () => void; disabled?: boolean }) {
  return <Confirm
    trigger={<Button variant="destructive" disabled={disabled}><Trash2 className="h-4 w-4" />삭제</Button>}
    title="Watch를 삭제할까요?"
    description="Watch 삭제는 앞으로의 방송 감시를 종료하고 Watch에 저장된 비밀 값을 지웁니다. 이 Watch로 만들어진 녹화는 삭제되지 않으며, 현재 진행 중인 녹화도 중지되지 않습니다."
    confirmLabel="Watch 삭제"
    destructive
    onConfirm={onDelete}
    disabled={disabled}
  />
}
