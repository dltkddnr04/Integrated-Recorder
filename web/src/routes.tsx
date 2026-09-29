import { lazy } from 'react'
import { createRootRouteWithContext, createRoute, createRouter, redirect } from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { RootComponent, RootError } from '@/components/route-shell'
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
const WatchesPage = lazy(() => import('@/routes/watches').then(module => ({ default: module.WatchesPage })))
const WatchNewPage = lazy(() => import('@/routes/watch-new').then(module => ({ default: module.WatchNewPage })))
const WatchDetailPage = lazy(() => import('@/routes/watch-detail').then(module => ({ default: module.WatchDetailPage })))
const StoragePage = lazy(() => import('@/routes/storage').then(module => ({ default: module.StoragePage })))
const StoragePoolDetailPage = lazy(() => import('@/routes/storage').then(module => ({ default: module.StoragePoolDetailPage })))
const LoginPage = lazy(() => import('@/routes/login').then(module => ({ default: module.LoginPage })))
import { NotFoundPage } from '@/routes/not-found'
import { isRecordingState } from '@/types/api'

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
const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: '/login', validateSearch: (search: Record<string, unknown>): { mode?: 'bootstrap' } => search.mode === 'bootstrap' ? { mode: 'bootstrap' } : {}, beforeLoad: async ({ context }) => {
  const session = await context.queryClient.ensureQueryData(sessionQuery)
  if (session.authenticated && !session.needs_bootstrap) throw redirect({ to: '/' })
}, component: LoginPage })
const dashboardRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: DashboardPage })
const recordingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/recordings', validateSearch: (search: Record<string, unknown>): RecordRouteSearch => ({
  q: typeof search.q === 'string' ? search.q : undefined, state: isRecordingState(search.state) ? search.state : undefined,
  adapter: typeof search.adapter === 'string' ? search.adapter : undefined, resource_type: typeof search.resource_type === 'string' ? search.resource_type : undefined,
  started_after: typeof search.started_after === 'string' ? search.started_after : undefined, started_before: typeof search.started_before === 'string' ? search.started_before : undefined,
  has_gaps: typeof search.has_gaps === 'string' ? search.has_gaps : undefined, integrity: typeof search.integrity === 'string' ? search.integrity : undefined,
  tag: typeof search.tag === 'string' ? search.tag : undefined, sort: typeof search.sort === 'string' ? search.sort : '-started_at',
  limit: typeof search.limit === 'number' ? search.limit : 25, cursor: typeof search.cursor === 'string' ? search.cursor : undefined,
}), component: RecordingsPage })
const recordingRoute = createRoute({ getParentRoute: () => rootRoute, path: '/recordings/$recordingId', component: RecordingDetailPage })
const newRoute = createRoute({ getParentRoute: () => rootRoute, path: '/new', component: NewRecordingPage })
const adaptersRoute = createRoute({ getParentRoute: () => rootRoute, path: '/adapters', component: AdaptersPage })
const adapterRoute = createRoute({ getParentRoute: () => rootRoute, path: '/adapters/$adapterId', validateSearch: (search: Record<string, unknown>) => ({ tab: search.tab === 'resources' ? 'resources' as const : undefined }), component: AdapterDetailPage })
const workflowsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/workflows', component: WorkflowsPage })
const workflowRoute = createRoute({ getParentRoute: () => rootRoute, path: '/workflows/$workflowId', component: WorkflowDetailPage })
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: SettingsPage })
const watchesRoute = createRoute({ getParentRoute: () => rootRoute, path: '/watches', component: WatchesPage })
const watchNewRoute = createRoute({ getParentRoute: () => rootRoute, path: '/watches/new', component: WatchNewPage })
const watchDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/watches/$watchId', component: WatchDetailPage })
const storageRoute = createRoute({ getParentRoute: () => rootRoute, path: '/storage', component: StoragePage })
const storagePoolRoute = createRoute({ getParentRoute: () => rootRoute, path: '/storage/$poolId', component: StoragePoolDetailPage })

const routeTree = rootRoute.addChildren([loginRoute, dashboardRoute, recordingsRoute, recordingRoute, newRoute, adaptersRoute, adapterRoute, workflowsRoute, workflowRoute, watchesRoute, watchNewRoute, watchDetailRoute, storageRoute, storagePoolRoute, settingsRoute])
export const router = createRouter({ routeTree, context: { queryClient: undefined! }, defaultPreload: 'intent', defaultPreloadStaleTime: 0 })
declare module '@tanstack/react-router' { interface Register { router: typeof router } }
