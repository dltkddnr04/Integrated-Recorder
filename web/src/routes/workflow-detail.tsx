import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, ShieldQuestion } from 'lucide-react'
import { workflowsAPI } from '@/api'
import { qk } from '@/api/queries'
import { SchemaForm } from '@/components/schema-form'
import { PageHeading } from '@/components/page-heading'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Confirm } from '@/components/ui/confirm'
import { EmptyState, ErrorState, LoadingState } from '@/components/query-state'
import { useToast } from '@/components/ui/use-toast'
import { resourceLabel } from '@/lib/utils'
import { errorMessage } from '@/lib/errors'
import { WorkflowPromptPanel } from '@/components/workflow/prompt-panel'
import type { RecordingDetail, Schema, WorkflowProgress } from '@/types/api'

export function WorkflowDetailPage() {
  const { workflowId } = useParams({ from: '/workflows/$workflowId' }); const client = useQueryClient(); const navigate = useNavigate(); const { toast } = useToast()
  const workflow = useQuery({ queryKey: qk.workflow(workflowId), queryFn: () => workflowsAPI.get(workflowId), retry: false })
  const [errorText, setErrorText] = useState('')
  const cancel = useMutation({ mutationFn: () => workflowsAPI.cancel(workflowId), onSuccess: async () => { toast('워크플로를 취소했습니다.'); await client.invalidateQueries({ queryKey: qk.workflows }); await client.invalidateQueries({ queryKey: qk.workflowHistory }); await navigate({ to: '/workflows' }) }, onError: error => toast('취소 실패', errorMessage(error), 'error') })
  const proceed = useMutation({ mutationFn: (body: { values: Record<string, unknown>; secrets: Record<string, string>; persist_fields: string[] }) => workflowsAPI.continue(workflowId, body), onSuccess: async (result: WorkflowProgress | RecordingDetail) => {
    if ('id' in result) { toast('Resolution이 완료되어 녹화를 시작했습니다.'); await client.invalidateQueries({ queryKey: qk.dashboard }); await client.invalidateQueries({ queryKey: ['recordings'] }); await navigate({ to: '/recordings/$recordingId', params: { recordingId: result.id } }); return }
    setErrorText(''); client.setQueryData(qk.workflow(workflowId), result); await client.invalidateQueries({ queryKey: qk.workflows }); await client.invalidateQueries({ queryKey: qk.workflowHistory }); toast('Workflow가 업데이트되었습니다.', result.state, 'info')
  }, onError: error => setErrorText(errorMessage(error)) })
  if (workflow.isLoading) return <LoadingState />
  if (workflow.error || !workflow.data) return <div className="page-enter"><PageHeading title="워크플로를 찾을 수 없습니다" description="만료되었거나 이미 종료된 workflow입니다." actions={<Link to="/workflows"><Button variant="outline"><ArrowLeft className="h-4 w-4" />워크플로 목록</Button></Link>} /><ErrorState message={errorMessage(workflow.error ?? 'workflow unavailable')} retry={() => void workflow.refetch()} /></div>
  const item = workflow.data; const challengeSchema: Schema = item.challenge?.schema ?? { fields: [] }; const prompt = item.challenge?.prompt
  const submit = (result: { values: Record<string, unknown>; secrets: Record<string, string>; persistFields: string[] }) => proceed.mutate({ values: result.values, secrets: result.secrets, persist_fields: result.persistFields })
  const terminalPrompt = prompt?.type === 'complete' || prompt?.type === 'error'
  return <div className="page-enter"><PageHeading eyebrow="ADAPTER WORKFLOW" title="추가 정보 요청" description={`${item.adapter_id} · ${item.workflow_id}`} actions={<><Link to="/workflows"><Button variant="outline"><ArrowLeft className="h-4 w-4" />워크플로 목록</Button></Link><Confirm trigger={<Button variant="destructive"><span>취소</span></Button>} title="Workflow를 취소할까요?" description="현재 adapter resolution을 취소합니다. 입력한 answer는 저장되지 않습니다." confirmLabel="워크플로 취소" destructive onConfirm={() => cancel.mutate()} disabled={cancel.isPending || terminalPrompt} /></>} />
    <div className="grid gap-4 xl:grid-cols-[1fr_360px]"><Card><CardHeader><CardTitle className="flex items-center gap-2"><ShieldQuestion className="h-5 w-5 text-primary" />{prompt?.title ?? 'Adapter 입력이 필요합니다'}</CardTitle><p className="text-sm leading-6 text-muted-foreground">{prompt?.message ?? 'Adapter에서 요청한 정보를 입력해 resolution을 이어가세요.'}</p></CardHeader><CardContent>{prompt && <WorkflowPromptPanel prompt={prompt} />}{challengeSchema.fields.length && !terminalPrompt ? <SchemaForm key={`${workflowId}-${item.state}`} schema={challengeSchema} mode="challenge" submitLabel="계속 진행" onSubmit={submit} busy={proceed.isPending} /> : item.state === 'resolved' || prompt?.type === 'complete' ? <EmptyState title="Resolution이 완료되었습니다." /> : terminalPrompt ? null : <EmptyState title="입력 가능한 challenge가 없습니다." description="이 workflow 상태를 확인하거나 취소하세요." />}{errorText && <p role="alert" className="mt-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{errorText}</p>}{proceed.isPending && <p className="mt-3 text-xs text-muted-foreground">Adapter resolution 진행 중…</p>}</CardContent></Card><div className="space-y-4"><Card><CardHeader><CardTitle>진행 상태</CardTitle></CardHeader><CardContent><StatusBadge state={item.state} /><dl className="mt-4 space-y-3">{[['Adapter', item.adapter_id], ['Resource', item.resource ? resourceLabel(item.resource) : '발견 대기'], ['Workflow ID', item.workflow_id]].map(([key, value]) => <div key={key}><dt className="text-[11px] text-muted-foreground">{key}</dt><dd className="mt-1 break-all text-xs font-medium">{value}</dd></div>)}</dl></CardContent></Card><Card><CardHeader><CardTitle>안전 정보</CardTitle></CardHeader><CardContent className="space-y-2 text-xs leading-5 text-muted-foreground"><p>Secret 답변은 표시하거나 history에 저장하지 않습니다.</p><p>저장 여부와 target scope는 각 field의 adapter-declared persistence policy에 따릅니다.</p><p>Process 재시작 또는 만료 후에는 workflow를 다시 시작해야 할 수 있습니다.</p></CardContent></Card></div></div>
  </div>
}
