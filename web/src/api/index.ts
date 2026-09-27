import { api, queryString } from './client'
import type { Adapter, AdapterConfig, AdapterDescriptor, ApiSession, ArchiveEntry, AuditEvent, Dashboard, ExportJob, IntegrityJob, IntegrityResult, LogEntry, Notification, RecordingDetail, RecordingEvent, RecordingPage, Resource, ResourceRef, Schema, SearchResult, StorageInfo, SystemInfo, SystemSettings, WorkflowHistoryEvent, WorkflowProgress, WorkflowSummary } from '@/types/api'

export const authAPI = {
  session: () => api<ApiSession>('/api/auth/session'),
  bootstrap: (token: string, password: string) => api<ApiSession>('/api/auth/bootstrap', { method: 'POST', body: { token, password } }),
  login: (password: string) => api<ApiSession>('/api/auth/login', { method: 'POST', body: { password } }),
  logout: () => api<void>('/api/auth/logout', { method: 'POST' }),
}
export const dashboardAPI = {
  get: () => api<Dashboard>('/api/dashboard'), storage: () => api<StorageInfo>('/api/system/storage'), info: () => api<SystemInfo>('/api/system/info'),
}
export type RecordingQuery = { q?: string; state?: string; adapter?: string; resource_type?: string; started_after?: string; started_before?: string; has_gaps?: string; integrity?: string; tag?: string; sort?: string; limit?: number; cursor?: string }
function encodeRef(ref: ResourceRef) { return btoa(unescape(encodeURIComponent(JSON.stringify(ref)))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') }
export const recordingsAPI = {
  list: (query: RecordingQuery) => api<RecordingPage>(`/api/v2/recordings${queryString(query)}`),
  get: (id: string) => api<RecordingDetail>(`/api/recordings/${encodeURIComponent(id)}`),
  start: (body: { adapter_id: string; input: Record<string, unknown>; resource?: ResourceRef; title?: string }) => api<RecordingDetail | WorkflowProgress>('/api/recordings', { method: 'POST', body }),
  stop: (id: string) => api<RecordingDetail>(`/api/recordings/${encodeURIComponent(id)}/stop`, { method: 'POST' }),
  remove: (id: string) => api<void>(`/api/recordings/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  tags: (id: string) => api<{ tags: string[] }>(`/api/recordings/${encodeURIComponent(id)}/tags`),
  setTags: (id: string, tags: string[]) => api<{ tags: string[] }>(`/api/recordings/${encodeURIComponent(id)}/tags`, { method: 'PUT', body: { tags } }),
  archive: (id: string) => api<{ recording_id: string; entries: ArchiveEntry[] }>(`/api/recordings/${encodeURIComponent(id)}/archive/index`),
  events: (id: string) => api<{ items: RecordingEvent[] }>(`/api/recordings/${encodeURIComponent(id)}/events`),
  thumbnail: (id: string) => `/api/recordings/${encodeURIComponent(id)}/thumbnail`,
  regenerateThumbnail: (id: string) => api<unknown>(`/api/recordings/${encodeURIComponent(id)}/thumbnail/regenerate`, { method: 'POST' }),
}
export const integrityAPI = {
  get: (id: string) => api<IntegrityResult>(`/api/recordings/${encodeURIComponent(id)}/integrity`),
  start: (id: string) => api<IntegrityJob>(`/api/recordings/${encodeURIComponent(id)}/integrity/verify`, { method: 'POST' }),
  job: (id: string) => api<IntegrityJob>(`/api/integrity/jobs/${encodeURIComponent(id)}`),
  cancel: (id: string) => api<IntegrityJob>(`/api/integrity/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
}
export const derivativeAPI = {
  exports: (id: string) => api<{ items: ExportJob[]; available: boolean }>(`/api/recordings/${encodeURIComponent(id)}/exports`),
  create: (id: string) => api<ExportJob>(`/api/recordings/${encodeURIComponent(id)}/exports`, { method: 'POST', body: { format: 'mkv' } }),
  remove: (id: string) => api<void>(`/api/exports/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  download: (id: string) => `/api/exports/${encodeURIComponent(id)}/download`,
}
export const adaptersAPI = {
  list: () => api<Adapter[]>('/api/adapters'), get: (id: string) => api<Adapter>(`/api/adapters/${encodeURIComponent(id)}`),
  schema: (id: string, resource?: ResourceRef) => api<{ input_schema: Schema; configuration_schema: Schema; resource_types?: AdapterDescriptor['resource_types']; media_types: string[] }>(`/api/adapters/${encodeURIComponent(id)}/schema${resource ? queryString({ resource: encodeRef(resource) }) : ''}`),
  config: (id: string, resource?: ResourceRef) => api<AdapterConfig>(`/api/adapters/${encodeURIComponent(id)}/config${resource ? queryString({ resource: encodeRef(resource) }) : ''}`),
  saveConfig: (id: string, body: { resource?: ResourceRef; values?: Record<string, unknown>; secrets?: Record<string, string>; clear_values?: string[]; clear_secrets?: string[] }) => api<AdapterConfig>(`/api/adapters/${encodeURIComponent(id)}/config`, { method: 'PUT', body }),
  action: (id: string, action: 'restart' | 'enable' | 'disable') => api<Adapter>(`/api/adapters/${encodeURIComponent(id)}/${action}`, { method: 'POST' }),
  resources: (id: string, options: { q?: string; parent?: ResourceRef; resource_type?: string; cursor?: string; limit?: number }) => api<{ items: Resource[]; next_cursor?: string }>(`/api/adapters/${encodeURIComponent(id)}/resources${options.q ? `/search${queryString({ q: options.q, parent: options.parent ? encodeRef(options.parent) : undefined, resource_type: options.resource_type, cursor: options.cursor, limit: options.limit })}` : queryString({ parent: options.parent ? encodeRef(options.parent) : undefined, resource_type: options.resource_type, cursor: options.cursor, limit: options.limit })}`),
}
export const workflowsAPI = {
  list: () => api<WorkflowSummary[]>('/api/resolve-workflows'), get: (id: string) => api<WorkflowProgress>(`/api/resolve-workflows/${encodeURIComponent(id)}`),
  continue: (id: string, body: { values?: Record<string, unknown>; secrets?: Record<string, string>; persist_fields?: string[] }) => api<WorkflowProgress | RecordingDetail>(`/api/resolve-workflows/${encodeURIComponent(id)}/continue`, { method: 'POST', body }),
  cancel: (id: string) => api<void>(`/api/resolve-workflows/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  history: () => api<{ items: WorkflowHistoryEvent[] }>('/api/workflow-history'),
}
export const productAPI = {
  search: (q: string) => api<{ results: SearchResult[] }>(`/api/search${queryString({ q, limit: 20 })}`),
  notifications: () => api<{ items: Notification[] }>('/api/notifications'),
  markRead: (id: string) => api<void>(`/api/notifications/${encodeURIComponent(id)}/read`, { method: 'POST' }),
  markAllRead: () => api<void>('/api/notifications/read-all', { method: 'POST' }),
  syncNotifications: () => api<unknown>('/api/notifications/sync', { method: 'POST' }),
  audit: () => api<{ items: AuditEvent[] }>('/api/audit'),
  logs: (query: { level?: string; component?: string; q?: string; limit?: number; cursor?: string }) => api<{ items: LogEntry[]; next_cursor?: string }>(`/api/logs${queryString(query)}`),
  settings: () => api<SystemSettings>('/api/settings'),
  saveSettings: (body: { ui?: { theme: 'system' | 'light' | 'dark' }; integrity?: { concurrency: number }; retention?: { enabled?: boolean; completed_after_days?: number } }) => api<SystemSettings>('/api/settings', { method: 'PUT', body }),
  retentionCandidates: () => api<{ enabled: boolean; candidate_count: number; candidates: { id: string; stopped_at: string }[] }>('/api/retention/candidates'),
  runRetention: () => api<{ candidate_count: number; deleted_count: number; deleted_ids: string[] }>('/api/retention/run', { method: 'POST', body: {} }),
}
