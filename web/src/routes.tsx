import { lazy, Suspense } from 'react'
import { createRootRouteWithContext, createRoute, createRouter, Outlet, redirect, useRouterState } from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { AppShell } from '@/components/layout/app-shell'
import { sessionQuery } from '@/api/queries'
const DashboardPage = lazy(() => import('@/routes/dashboard').then(module => ({ default: module.DashboardPage })))
const RecordingsPage = lazy(() => import('@/routes/recordings').then(module => ({ default: module.RecordingsPage })))
const RecordingDetailPage = lazy(() => import('@/routes/recording-detail').then(module => ({ default: module.RecordingDetailPage })))
const NewRecordingPage = lazy(() => import('@/routes/new-recording').then(module => ({ default: module.NewRecordingPage })))
const AdaptersPage = lazy(() => import('@/routes/adapters').then(module => ({ default: module.AdaptersPage })))
const AdapterDetailPage = lazy(() => import('@/routes/adapter-detail').then(module => ({ default: module.AdapterDetailPage })))
const WorkflowsPage = lazy(() => import('@/routes/workflows').then(module => ({ default: module.WorkflowsPage })))
const WorkflowDetailPage = lazy(() => import('@/routes/workflow-detail').then(module => ({ default: module.WorkflowDetailPage })))
const SettingsPage = lazy(() => import('@/routes/settings').then(module => ({ default: module.SettingsPage })))
const LoginPage = lazy(() => import('@/routes/login').then(module => ({ default: module.LoginPage })))
import { NotFoundPage } from '@/routes/not-found'
import { Button } from '@/components/ui/button'

type RouterContext = { queryClient: QueryClient }
type RecordRouteSearch = { q?: string; state?: string; adapter?: string; resource_type?: string; started_after?: string; started_before?: string; has_gaps?: string; integrity?: string; tag?: string; sort?: string; limit?: number; cursor?: string }
const rootRoute = createRootRouteWithContext<RouterContext>()({
  beforeLoad: async ({ context, location }) => {
    const path = location.pathname
    if (path === '/login') return
    const session = await context.queryClient.ensureQueryData(sessionQuery)
    if (session.needs_bootstrap || !session.authenticated) throw redirect({ to: '/login', search: session.needs_bootstrap ? { mode: 'bootstrap' } : {} })
  },
  component: RootComponent,
  notFoundComponent: NotFoundPage,
  errorComponent: RootError,
})
function RootComponent() {
  const pathname = useRouterState({ select: state => state.location.pathname })
  return <Suspense fallback={<div className="grid min-h-screen place-items-center bg-background p-6 text-sm text-muted-foreground" role="status">화면 불러오는 중…</div>}>{pathname === '/login' ? <Outlet /> : <AppShell />}</Suspense>
}
function RootError({ reset }: { reset: () => void }) {
  return <main className="grid min-h-screen place-items-center bg-background p-5"><section className="w-full max-w-lg rounded-xl border border-border bg-card p-6 text-center shadow-sm" role="alert"><h1 className="text-lg font-semibold">화면을 불러오지 못했습니다</h1><p className="mt-2 text-sm text-muted-foreground">관리 API 연결을 확인하고 다시 시도하세요.</p><Button className="mt-5" onClick={reset}>다시 시도</Button></section></main>
}

const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: '/login', validateSearch: (search: Record<string, unknown>): { mode?: 'bootstrap' } => search.mode === 'bootstrap' ? { mode: 'bootstrap' } : {}, beforeLoad: async ({ context }) => {
  const session = await context.queryClient.ensureQueryData(sessionQuery)
  if (session.authenticated && !session.needs_bootstrap) throw redirect({ to: '/' })
}, component: LoginPage })
const dashboardRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: DashboardPage })
const recordingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/recordings', validateSearch: (search: Record<string, unknown>): RecordRouteSearch => ({
  q: typeof search.q === 'string' ? search.q : undefined, state: typeof search.state === 'string' ? search.state : undefined,
  adapter: typeof search.adapter === 'string' ? search.adapter : undefined, resource_type: typeof search.resource_type === 'string' ? search.resource_type : undefined,
  started_after: typeof search.started_after === 'string' ? search.started_after : undefined, started_before: typeof search.started_before === 'string' ? search.started_before : undefined,
  has_gaps: typeof search.has_gaps === 'string' ? search.has_gaps : undefined, integrity: typeof search.integrity === 'string' ? search.integrity : undefined,
  tag: typeof search.tag === 'string' ? search.tag : undefined, sort: typeof search.sort === 'string' ? search.sort : '-started_at',
  limit: typeof search.limit === 'number' ? search.limit : 25, cursor: typeof search.cursor === 'string' ? search.cursor : undefined,
}), component: RecordingsPage })
const recordingRoute = createRoute({ getParentRoute: () => rootRoute, path: '/recordings/$recordingId', component: RecordingDetailPage })
const newRoute = createRoute({ getParentRoute: () => rootRoute, path: '/new', component: NewRecordingPage })
const adaptersRoute = createRoute({ getParentRoute: () => rootRoute, path: '/adapters', component: AdaptersPage })
const adapterRoute = createRoute({ getParentRoute: () => rootRoute, path: '/adapters/$adapterId', component: AdapterDetailPage })
const workflowsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/workflows', component: WorkflowsPage })
const workflowRoute = createRoute({ getParentRoute: () => rootRoute, path: '/workflows/$workflowId', component: WorkflowDetailPage })
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: SettingsPage })

const routeTree = rootRoute.addChildren([loginRoute, dashboardRoute, recordingsRoute, recordingRoute, newRoute, adaptersRoute, adapterRoute, workflowsRoute, workflowRoute, settingsRoute])
export const router = createRouter({ routeTree, context: { queryClient: undefined! }, defaultPreload: 'intent', defaultPreloadStaleTime: 0 })
declare module '@tanstack/react-router' { interface Register { router: typeof router } }
