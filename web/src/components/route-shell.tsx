import { Suspense } from 'react'
import { Outlet, useRouterState } from '@tanstack/react-router'
import { AppShell } from '@/components/layout/app-shell'
import { Button } from '@/components/ui/button'

export function RootComponent() {
  const pathname = useRouterState({ select: state => state.location.pathname })
  return <Suspense fallback={<div className="grid min-h-screen place-items-center bg-background p-6 text-sm text-muted-foreground" role="status">화면 불러오는 중…</div>}>{pathname === '/login' || pathname === '/setup' ? <Outlet /> : <AppShell />}</Suspense>
}

export function RootError({ reset }: { reset: () => void }) {
  return <main className="grid min-h-screen place-items-center bg-background p-5"><section className="w-full max-w-lg rounded-xl border border-border bg-card p-6 text-center shadow-sm" role="alert"><h1 className="text-lg font-semibold">화면을 불러오지 못했습니다</h1><p className="mt-2 text-sm text-muted-foreground">관리 API 연결을 확인하고 다시 시도하세요.</p><Button className="mt-5" onClick={reset}>다시 시도</Button></section></main>
}
