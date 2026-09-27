export type ResourceRef = { resource_type: string; resource_id: string; parent?: ResourceRef }
export type Resource = ResourceRef & { display_name?: string; attributes?: Record<string, unknown> }
export type FieldControl = 'text' | 'secret' | 'number' | 'boolean' | 'select' | 'multi-select' | 'textarea' | 'action' | 'status'
export type Option = { value: unknown; label: string }
export type FieldPersistence = { mode: 'forbidden' | 'optional' | 'required'; target: { scope: 'plugin' | 'current_resource' | 'resource'; resource?: ResourceRef } }
export type SchemaField = {
  key: string; control: FieldControl; label: string; description?: string; required?: boolean; inherit?: boolean;
  default?: unknown; constraints?: { min?: number; max?: number; min_length?: number; max_length?: number; pattern?: string; min_items?: number; max_items?: number };
  options?: Option[]; visible_when?: unknown; persistence?: FieldPersistence
}
export type Schema = { fields: SchemaField[] }
export type AdapterDescriptor = {
  id: string; name: string; version: string; protocol_version: number; capabilities?: string[];
  input_schema: Schema; configuration_schema: Schema; resource_types?: { type: string; parent_types?: string[]; configuration_schema?: Schema }[]; media_types: string[]
}
export type AdapterStatus = { id: string; name?: string; version?: string; protocol_version?: number; state: string; error?: string; generation?: number; restart_attempts?: number }
export type Adapter = { descriptor?: AdapterDescriptor; status: AdapterStatus }
export type RecordingState = 'recording' | 'completed' | 'interrupted' | 'failed' | string
export type Segment = { sequence?: number; source_sequence?: number; source_epoch?: number; archive_ordinal?: number; duration?: number; storage_path?: string; payload_size?: number; sha256?: string; discontinuity?: boolean; init_segment_id?: string; program_date_time?: string }
export type Gap = { track_id?: string; source_epoch?: number; from_sequence?: number; to_sequence?: number; duration_seconds?: number; reason?: string }
export type Track = { id?: string; name?: string; type?: string; segments?: Segment[]; init_segments?: Segment[]; [key: string]: unknown }
export type RecordingSummary = {
  id: string; title?: string; adapter_id?: string; adapter?: { id: string; name?: string; version?: string; protocol_version?: number; fingerprint?: string };
  resource?: ResourceRef & { display_name?: string }; state: RecordingState; created_at: string; started_at: string; stopped_at?: string | null;
  track_count?: number; segment_count?: number; duration_seconds?: number; gap_count?: number; archive_size_bytes?: number;
  media_payload_size_bytes?: number; manifest_size_bytes?: number; init_payload_size_bytes?: number; init_segment_count?: number;
  manifest_snapshot_count?: number; gap_segment_count?: number; gap_duration_seconds?: number | null; integrity?: IntegrityStatus; tags?: string[]
}
export type RecordingDetail = RecordingSummary & {
  tracks?: Record<string, Track>; gaps?: Gap[];
  snapshots?: { storage_path?: string; size?: number; sha256?: string; captured_at?: string }[];
  source_uri_classification?: string; statistics?: RecordingSummary; last_error?: string
}
export type RecordingPage = { items: RecordingSummary[]; next_cursor?: string; total: number }
export type IntegrityStatus = 'unknown' | 'verifying' | 'verified' | 'degraded' | 'failed'
export type IntegrityResult = { status: IntegrityStatus; last_verified_at?: string; objects_total?: number; objects_verified?: number; objects_missing?: number; objects_corrupt?: number; issues?: { code: string; path?: string }[] }
export type IntegrityJob = { id: string; recording_id: string; state: 'queued' | 'running' | 'completed' | 'failed' | 'canceled'; created_at: string; started_at?: string; finished_at?: string; error_code?: string; result?: IntegrityResult }
export type ExportJob = { id: string; recording_id: string; state: string; format?: string; output_name?: string; created_at?: string; finished_at?: string; error_code?: string }
export type Notification = { id: string; type: string; at: string; read: boolean; object_id?: string }
export type WorkflowProgress = {
  workflow_id: string; adapter_id: string; state: string; resource?: ResourceRef;
  challenge?: { schema: Schema; prompt?: { type: string; title?: string; message?: string; fields?: SchemaField[]; data?: unknown }; persistable?: boolean };
  adapter?: { id: string; version: string; protocol_version: number }
}
export type WorkflowSummary = { workflow_id: string; adapter_id: string; state: string; resource?: ResourceRef; challenge?: { prompt_type?: string; field_count: number; has_secret_fields: boolean }; created_at: string; updated_at: string; expires_at: string; in_progress: boolean }
export type WorkflowHistoryEvent = { id: string; workflow_id: string; adapter_id: string; state: string; at: string; resource?: ResourceRef; challenge?: { title?: string; message?: string; field_count: number; has_secret_fields: boolean } }
export type ConfigView = { values: Record<string, unknown>; secrets: Record<string, { configured: boolean }> }
export type AdapterConfig = {
  schema: Schema; values: Record<string, unknown>; secrets: Record<string, { configured: boolean }>;
  stored: ConfigView; effective: ConfigView; value_sources: Record<string, string>; secret_sources: Record<string, string>; current_scope: string
}
export type Dashboard = {
  active_recordings_count: number; completed_last_24h: number; interrupted_last_24h: number; recordings_total: number;
  segments_total: number; gaps_total: number; archive_bytes: number; filesystem_total_bytes: number; filesystem_free_bytes: number; filesystem_used_bytes: number;
  integrity: Record<string, number>; adapters: Record<string, number>; export_available: boolean; recent_recordings: RecordingSummary[]; active_recordings: RecordingSummary[]
}
export type StorageInfo = { archive_root: string; filesystem_total_bytes: number; filesystem_used_bytes: number; filesystem_available_bytes: number; recordings_bytes: number; recording_count: number; segment_count: number; init_segment_count: number; manifest_count: number }
export type SystemInfo = { version: string; commit: string; go_version: string; goos: string; goarch: string; started_at: string; uptime_seconds: number; export_available: boolean }
export type SystemSettings = { settings: { ui: { theme: 'system' | 'light' | 'dark' }; integrity: { concurrency: number }; retention: { enabled: boolean; completed_after_days: number } }; restart_required: string[] }
export type SearchResult = { type: 'recording' | 'adapter' | 'resource' | 'workflow'; id?: string; workflow_id?: string; adapter_id?: string; resource_type?: string; resource_id?: string; title?: string; name?: string; display_name?: string; state?: string; resource?: ResourceRef }
export type ArchiveEntry = { kind: string; path: string; size: number; sha256?: string }
export type RecordingEvent = { id: string; recording_id: string; type: string; at: string; count?: number; message?: string }
export type LogEntry = { at: string; level: string; component: string; message: string }
export type AuditEvent = { id: string; type: string; at: string; object_id?: string }
export type ApiSession = { auth_enabled: boolean; authenticated: boolean; needs_bootstrap: boolean; bootstrap_token_path?: string; csrf_token?: string; expires_at?: string }
