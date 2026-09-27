import { AlertTriangle, CircleCheck, CircleDashed, CircleX, LoaderCircle, Radio, Square } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { humanize } from '@/lib/utils'

const stateTone: Record<string, 'neutral' | 'blue' | 'green' | 'amber' | 'red' | 'purple'> = { recording: 'green', stopped: 'neutral', completed: 'blue', interrupted: 'amber', failed: 'red', verified: 'green', degraded: 'amber', unknown: 'neutral', verifying: 'blue', queued: 'neutral', running: 'blue', ready: 'green', unavailable: 'amber', rejected: 'red', disabled: 'neutral' }
const icons: Record<string, typeof CircleDashed> = { recording: Radio, stopped: Square, completed: CircleCheck, interrupted: AlertTriangle, failed: CircleX, verified: CircleCheck, degraded: AlertTriangle, verifying: LoaderCircle, unknown: CircleDashed, queued: CircleDashed, running: LoaderCircle, ready: CircleCheck, unavailable: AlertTriangle, rejected: CircleX, disabled: Square }
const labels: Record<string, string> = { recording: '녹화 중', stopped: '중지됨', completed: '완료', interrupted: '중단됨' }
export function StatusBadge({ state }: { state?: string }) { const key = state?.toLowerCase() ?? 'unknown'; const Icon = icons[key] ?? CircleDashed; return <Badge tone={stateTone[key] ?? 'neutral'}><Icon className={`h-3 w-3 ${key === 'running' || key === 'verifying' ? 'animate-spin' : ''}`} />{labels[key] ?? humanize(key)}</Badge> }
