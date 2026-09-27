import { queryOptions } from '@tanstack/react-query'
import { adaptersAPI, authAPI, dashboardAPI, derivativeAPI, integrityAPI, productAPI, recordingsAPI, workflowsAPI, type RecordingQuery } from './index'

export const qk = {
  session: ['auth', 'session'] as const, dashboard: ['dashboard'] as const, storage: ['system', 'storage'] as const, info: ['system', 'info'] as const,
  settings: ['settings'] as const, recordings: (query: RecordingQuery) => ['recordings', query] as const,
  recording: (id: string) => ['recording', id] as const, tags: (id: string) => ['recording', id, 'tags'] as const,
  archive: (id: string) => ['recording', id, 'archive'] as const, events: (id: string) => ['recording', id, 'events'] as const,
  integrity: (id: string) => ['recording', id, 'integrity'] as const, exports: (id: string) => ['recording', id, 'exports'] as const,
  adapters: ['adapters'] as const, adapter: (id: string) => ['adapter', id] as const, schema: (id: string, resource?: unknown) => ['adapter', id, 'schema', resource] as const,
  config: (id: string, resource?: unknown) => ['adapter', id, 'config', resource] as const, resources: (id: string, query: unknown) => ['adapter', id, 'resources', query] as const,
  workflows: ['workflows'] as const, workflow: (id: string) => ['workflow', id] as const, workflowHistory: ['workflow-history'] as const,
  notifications: ['notifications'] as const, audit: ['audit'] as const, logs: (query: unknown) => ['logs', query] as const,
}
export const sessionQuery = queryOptions({ queryKey: qk.session, queryFn: authAPI.session, retry: false, staleTime: 10_000 })
export const adaptersQuery = queryOptions({ queryKey: qk.adapters, queryFn: adaptersAPI.list, staleTime: 15_000, refetchInterval: 20_000, refetchIntervalInBackground: false })
export const dashboardQuery = queryOptions({ queryKey: qk.dashboard, queryFn: dashboardAPI.get, staleTime: 10_000, refetchInterval: 15_000, refetchIntervalInBackground: false })
export const notificationsQuery = queryOptions({ queryKey: qk.notifications, queryFn: productAPI.notifications, staleTime: 15_000, refetchInterval: 30_000, refetchIntervalInBackground: false })
export const workflowsQuery = queryOptions({ queryKey: qk.workflows, queryFn: workflowsAPI.list, staleTime: 5_000, refetchInterval: query => query.state.data?.some(workflow => workflow.in_progress) ? 8_000 : false, refetchIntervalInBackground: false })
export const recordingQuery = (id: string) => queryOptions({ queryKey: qk.recording(id), queryFn: () => recordingsAPI.get(id) })
export const integrityQuery = (id: string) => queryOptions({ queryKey: qk.integrity(id), queryFn: () => integrityAPI.get(id), staleTime: 2000 })
export const exportsQuery = (id: string) => queryOptions({ queryKey: qk.exports(id), queryFn: () => derivativeAPI.exports(id), staleTime: 2000 })
