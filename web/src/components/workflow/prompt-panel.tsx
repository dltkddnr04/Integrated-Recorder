import { useMemo } from 'react'
import { ExternalLink, Workflow } from 'lucide-react'
import type { WorkflowPrompt } from '@/types/api'
import { safeExternalURL } from '@/lib/urls'

export function WorkflowPromptPanel({ prompt }: { prompt: WorkflowPrompt }) {
  const data = useMemo(() => prompt.data && typeof prompt.data === 'object' && !Array.isArray(prompt.data) ? prompt.data as Record<string, unknown> : {}, [prompt.data])
  const url = safeExternalURL(typeof data.url === 'string' ? data.url : '')
  const message = prompt.message || prompt.title || ''
  if (prompt.type === 'navigate' || prompt.type === 'action') {
    const label = prompt.type === 'navigate' ? '외부 페이지 열기' : typeof data.label === 'string' && data.label.trim() ? data.label : '계속하기'
    return <section className="mb-4 rounded-md border border-blue-200 bg-blue-50 p-3 text-sm dark:border-blue-900 dark:bg-blue-950/20" aria-label={prompt.type === 'navigate' ? '외부 페이지 안내' : '어댑터 동작 안내'}>
      <div className="flex gap-2"><Workflow className="mt-0.5 h-4 w-4 shrink-0 text-primary" /><div className="min-w-0"><p>{message || (prompt.type === 'navigate' ? '어댑터가 외부 페이지 확인을 요청했습니다.' : '어댑터에서 다음 동작을 요청했습니다.')}</p>
        {url ? <a className="mt-2 inline-flex items-center gap-1 font-medium text-primary underline" href={url} target="_blank" rel="noopener noreferrer">{label}<ExternalLink className="h-3.5 w-3.5" /></a> : prompt.type === 'action' && <p className="mt-2 text-xs text-muted-foreground">이 동작에 사용할 안전한 외부 URL이 제공되지 않았습니다.</p>}
      </div></div>
    </section>
  }
  if (prompt.type === 'display' || prompt.type === 'status' || prompt.type === 'prompt' || prompt.type === 'secret_prompt') return <div className="mb-4 flex gap-2 rounded-md bg-muted p-3 text-sm" role="status"><Workflow className="mt-0.5 h-4 w-4 shrink-0 text-primary" /><span>{message || promptFallback[prompt.type]}</span></div>
  if (prompt.type === 'complete') return <div className="mb-4 rounded-md border border-emerald-300 bg-emerald-50 p-3 text-sm text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-100" role="status">{message || '요청이 완료되었습니다.'}</div>
  if (prompt.type === 'error') return <div className="mb-4 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive" role="alert">{message || '어댑터 요청을 완료하지 못했습니다.'}</div>
  return <div className="mb-4 rounded-md bg-muted p-3 text-sm" role="status">{message || '지원하지 않는 어댑터 안내입니다.'}</div>
}

const promptFallback: Record<string, string> = {
  prompt: '요청한 정보를 아래 양식에 입력하세요.',
  secret_prompt: '비밀 정보를 아래 양식에 입력하세요.',
  display: '어댑터 안내',
  status: '어댑터 상태',
}
