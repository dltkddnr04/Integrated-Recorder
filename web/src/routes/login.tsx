import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowRight, KeyRound, ShieldCheck } from 'lucide-react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { authAPI } from '@/api'
import { qk, sessionQuery } from '@/api/queries'
import { APIError } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useToast } from '@/components/ui/use-toast'
import { AppBrand } from '@/components/layout/app-brand'

export function LoginPage() {
  const search = useSearch({ from: '/login' })
  const { data: session, isLoading, error } = useQuery(sessionQuery)
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [token, setToken] = useState('')
  const [formError, setFormError] = useState('')
  const client = useQueryClient()
  const navigate = useNavigate()
  const { toast } = useToast()
  const bootstrap = Boolean(search.mode === 'bootstrap' || session?.needs_bootstrap)
  const mutation = useMutation({ mutationFn: () => bootstrap ? authAPI.bootstrap(token, password) : authAPI.login(password), onSuccess: async result => { client.setQueryData(qk.session, result); await client.invalidateQueries({ queryKey: qk.session }); toast(bootstrap ? '관리자 계정이 준비되었습니다.' : '로그인했습니다.'); await navigate({ to: '/' }) }, onError: error => { const message = error instanceof APIError ? error.message : '인증 요청을 처리하지 못했습니다.'; setFormError(message) } })
  const submit = (event: React.FormEvent) => { event.preventDefault(); setFormError(''); if (!password) return setFormError('비밀번호를 입력하세요.'); if (bootstrap && password !== confirm) return setFormError('비밀번호 확인이 일치하지 않습니다.'); if (bootstrap && !token.trim()) return setFormError('초기화 토큰을 입력하세요.'); mutation.mutate() }

  return <main className="grid min-h-screen place-items-center bg-background px-4 py-10"><div className="grid w-full max-w-5xl overflow-hidden rounded-2xl border border-border bg-card shadow-xl md:grid-cols-[1.05fr_.95fr]"><section className="relative hidden min-h-[560px] flex-col justify-between overflow-hidden bg-[#101c2e] p-10 text-white md:flex"><div className="login-hero-glow absolute inset-0 opacity-30" /><AppBrand variant="login" inverse className="relative" /><div className="relative max-w-md"><div className="mb-5 inline-flex items-center gap-2 rounded-full border border-white/15 bg-white/5 px-3 py-1.5 text-xs text-white/75"><ShieldCheck className="h-4 w-4 text-blue-300" />보관 데이터 관리</div><h1 className="text-4xl font-semibold leading-tight tracking-tight">Archive Live.<br /><span className="text-blue-300">Preserve Source.</span></h1><p className="mt-5 max-w-sm leading-7 text-white/60">원본 HLS 세그먼트를 보존하고, 언제든 다시 찾아 재생할 수 있도록 관리합니다.</p></div><div className="relative flex items-center gap-3 text-xs text-white/45"><span className="h-px w-8 bg-white/25" />원본은 기준으로 보존하고, 나머지는 파생 데이터로 관리합니다.</div></section><section className="flex min-h-[560px] items-center justify-center p-6 sm:p-10"><div className="w-full max-w-sm"><AppBrand variant="mobile" className="mb-8 md:hidden" /><p className="text-xs font-semibold tracking-[.18em] text-primary">{bootstrap ? '초기 설정' : '관리자 로그인'}</p><h2 className="mt-2 text-2xl font-semibold tracking-tight">{bootstrap ? '관리자 계정 설정' : '다시 오셨군요'}</h2><p className="mt-2 text-sm leading-6 text-muted-foreground">{bootstrap ? '이 서버를 관리할 초기 비밀번호를 설정하세요.' : '관리자 비밀번호를 입력해 녹화 보관 데이터를 관리하세요.'}</p>{bootstrap && session?.bootstrap_token_path && <div className="mt-5 rounded-lg border border-blue-200 bg-blue-50 p-3 text-xs leading-5 text-blue-800 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-200"><span className="font-semibold">초기화 토큰 위치</span><br /><code className="break-all">{session.bootstrap_token_path}</code><p className="mt-1 opacity-80">서버 데이터 디렉터리에서 토큰 파일을 확인하세요.</p></div>}{isLoading && <p className="mt-5 text-sm text-muted-foreground">세션을 확인하고 있습니다…</p>}{error && <p role="alert" className="mt-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">인증 서버에 연결할 수 없습니다.</p>}<form className="mt-6 space-y-4" onSubmit={submit}><div className="space-y-2">{bootstrap && <><Label htmlFor="bootstrap-token">초기화 토큰</Label><Input id="bootstrap-token" type="password" value={token} onChange={event => setToken(event.target.value)} autoComplete="one-time-code" required /></>}<Label htmlFor="password">관리자 비밀번호</Label><div className="relative"><KeyRound className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" /><Input id="password" className="pl-9" type="password" value={password} onChange={event => setPassword(event.target.value)} autoComplete={bootstrap ? 'new-password' : 'current-password'} required minLength={12} /></div></div>{bootstrap && <div className="space-y-2"><Label htmlFor="confirm-password">비밀번호 확인</Label><Input id="confirm-password" type="password" value={confirm} onChange={event => setConfirm(event.target.value)} autoComplete="new-password" required /></div>}{formError && <p role="alert" className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">{formError}</p>}<Button type="submit" className="w-full" disabled={mutation.isPending || isLoading}>{mutation.isPending ? '확인 중…' : bootstrap ? '서버 초기화' : '로그인'}<ArrowRight className="h-4 w-4" /></Button></form><p className="mt-6 text-center text-xs leading-5 text-muted-foreground">세션은 안전한 HttpOnly 쿠키로 유지됩니다.<br />비밀번호는 브라우저에 저장되지 않습니다.</p></div></section></div></main>
}
