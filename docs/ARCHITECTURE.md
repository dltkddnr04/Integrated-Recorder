# Integrated Recorder Architecture

[English README](../README.en.md) | [한국어 README](../README.md)

This document describes the current implementation. Reserved protocol shapes and future ideas are called out as such; they are not claims of implemented features.

## Core invariant

The canonical archive is the source media received from the broadcaster plus metadata needed to reconstruct its timeline. Acquisition does not decode, encode, transcode, or remux media. Core has no platform domain model: resource types, field keys, adapter state, and interaction data remain opaque strings or JSON values. Platform-specific discovery stays in an external adapter process.

## Runtime and package boundaries

```text
Browser ── HTTP API / generated VOD ── Core
                                          ├─ adapterhost ── framed JSON/stdin/stdout ── adapter binary
                                          ├─ shared HLS parser and acquisition
                                          ├─ network safety policy
                                          └─ self-describing recording directories
```

- `internal/adapterproto` defines language-neutral Protocol v1 envelopes, descriptor/schema validation, resources, workflows, media sources, refresh policy, and adapter-owned state messages.
- `internal/adapterhost` discovers only explicitly configured adapter directories, supervises long-lived processes, validates descriptors/resources, resolves settings, and manages workflow sessions.
- `internal/pluginconfig` stores user configuration/secrets separately from adapter-owned opaque state/state secrets. Interfaces are backend-neutral; the current implementation uses files.
- `internal/interaction` bounds and expires generic interaction progress messages.
- `cmd/adapters/owncast` and `internal/adapters/owncast` build the first standalone adapter. Core does not import the Owncast package.
- `internal/hls` parses the supported HLS subset. `internal/acquire` owns polling, refresh, retries, segment acquisition, sequence epochs, and recording lifecycle.
- `internal/network` validates public destinations and pins each connection to a freshly validated address.
- `internal/domain` holds archive-owned types independent of adapter wire types. `internal/storage` writes durable payloads and self-describing metadata. `internal/server` exposes the API and static management page.

## Adapter process and protocol

Adapters are standalone executables. Core scans only directories in `ADAPTER_DIR`, looking for executable regular files named `integrated-recorder-adapter-*`; it never scans `PATH`. Adding a binary does not require rebuilding Core, but discovery occurs at startup, so Core must restart. Installed adapters are trusted local code: they run as the Core OS user and are not sandboxed.

IPC is bounded newline-delimited JSON. V1 keeps its original untyped request/response wire form; request IDs, protocol version, method, result, and structured errors are validated. The parser also distinguishes typed frames and the reserved `notification` shape, but asynchronous notifications are not delivered by the runtime. A notification received where a response is expected is a protocol error.

The host serializes calls to each process. Timeout, cancellation after a request is written, malformed/oversized output, unexpected EOF, request-ID mismatch, protocol mismatch, or process exit makes that process unusable. The next operation may restart it lazily after bounded backoff. Every restart performs `describe`; adapter ID, version, protocol version, and the canonical descriptor fingerprint must match the original discovery descriptor. There is no background restart loop. Shutdown sends the generic shutdown request and then terminates the child within a bounded period.

V1 implements `describe`, legacy `resolve`, `resolve.begin`, `resolve.continue`, `refresh`, and `shutdown` where the descriptor advertises the relevant generic capability. Other operation names remain reserved or return a structured unsupported error. Unknown optional capability strings with valid syntax are retained and ignored by older Core versions; protocol-version mismatch remains fatal.

## Resources, configuration, and workflows

An adapter descriptor declares opaque resource types and allowed parent-type edges. Core validates each resource reference and every edge against that declaration for API hints, config scopes, workflow discoveries, and persistence targets. Core does not interpret the type names. The active chain is bounded in depth and cannot contain a cycle.

Configuration has distinct stored and effective views. Effective values are composed from plugin scope, parent-most resource, each child, and the current resource; more-specific values override less-specific ones. A field's `inherit` flag controls whether ancestor values or secrets flow downward. It defaults to `false` for every control. `clear_values` removes an override and returns that key to inherited/default behavior; `clear_secrets` explicitly removes a secret. Empty ordinary values are values, while a blank secret submission leaves the stored secret unchanged.

`resolve.begin` accepts adapter-defined input before a stable resource is known. The adapter may return a resource, after which Core validates its chain and loads matching effective configuration, secrets, and adapter state before continuing. Challenges use the same validated schema vocabulary and generic workflow state. Each challenge field can declare persistence as `forbidden`, `optional`, or `required`, plus a `plugin`, `current_resource`, or explicit `resource` target. Explicit resource targets must be members of the validated chain. Persistent challenge fields must also be declared in the target scope's descriptor schema with the same control; runtime-only fields are ephemeral. This prevents a saved value from becoming invisible to later resolutions.

Workflow sessions are process-local and bound to the adapter process generation that created them. A process restart expires a stale workflow deterministically instead of forwarding its ID to a fresh process. Sessions have a 30-minute idle TTL, a 128-session bound, and a 32-transition cumulative limit. `DELETE /api/resolve-workflows/{id}` cancels a session. Expiration/cancellation removes its title and interaction progress. Workflow sessions do not survive a Core restart.

The browser uses one schema renderer for recording input, settings, and challenges. It handles the declared `visible_when` grammar (`field` with `equals`, `not_equals`, or boolean `truthy`, recursively combined by `all`/`any`), defaults, select/multi-select values, inherited sources, and explicit clear operations. Secrets are never read back as plaintext. Prompt/display/status data is rendered as text; navigation accepts only HTTP(S) URLs and opens with `noopener noreferrer`. Adapter HTML and scripts are never executed.

## Adapter-owned state and refresh

Adapter-owned opaque state is separate from user configuration. State values and state secrets are stored under adapter and optional resource scopes; Core does not interpret their keys. Mutations are accepted only for scopes in the current validated chain. State is supplied to later resolve/refresh calls and persists across adapter process restarts. It is not included in recording metadata or public API responses.

The default file state-secret backend is separate from ordinary state and uses restricted permissions, but provides **no at-rest encryption**. The user secret backend has the same plaintext-at-rest limitation. Both are backend-neutral so a later encrypted or OS-backed implementation can replace them. Neither should be described as an encrypted vault.

An adapter with the refresh capability may declare an expiry time, refresh lead time, and HTTP status codes that should trigger refresh. Core does not infer token expiry from status codes. It performs proactive refresh and adapter-declared status refresh; adapter code supplies the replacement media source. Core validates its media/request policy and public manifest URL before committing staged adapter-state changes and swapping the active URL, headers, forwarding policy, and refresh policy. A failed validation/refresh does not replace the active source. Refresh errors exposed outside Core are generic and omit signed URLs and adapter secrets.

## Media acquisition and HLS support

The adapter resolves input into a generic media source. Core owns playlist parsing, rendition selection, request/header policy, retries, exact segment-byte storage, SHA-256, gap detection, and VOD generation. Adapter-supplied headers default to same-origin forwarding. An adapter may declare an exact origin allowlist; Core reapplies that decision on each request/redirect and independently enforces SSRF/public-address validation.

The current HLS subset handles a single self-contained rendition, MPEG-TS or fMP4 media objects, init maps, byte ranges with explicit safe range arithmetic, discontinuities, program date/time, and completed `EXTINF` segments. It can ignore LL-HLS partial tags when the same playlist contains complete segments. It explicitly rejects encrypted HLS, external audio/video/subtitle renditions, I-frame-only playlists, URI variable substitution (`EXT-X-DEFINE`), delta (`EXT-X-SKIP`) playlists, and partial-only LL-HLS. DASH, subtitles as separate renditions, and DRM/key acquisition are not implemented. Rejections are deterministic; source media bytes are not rewritten.

Media sequence numbers are source identifiers, not archive identity. Core tracks source epochs and a monotonically increasing archive ordinal so resets do not suppress later media with reused sequence numbers. VOD ordering uses archive ordinals and inserts discontinuities at epoch boundaries or detected gaps.

## Storage, privacy, and recovery

Each recording is a self-describing directory containing `recording.json`, raw manifest snapshots, payloads, and segment sidecars. The directory is the canonical source; there is no required database. A new directory is first written under a hidden incomplete name with initial metadata and then atomically published. Files are synced before rename and parent directories are synced where supported.

Segment and manifest payloads are written before their sidecars, and the sidecars before the root recording document is updated. On startup, valid sidecars can restore a segment or manifest snapshot omitted from the root document after a crash. Payload without a sidecar is preserved and reported as an orphan; corrupt or conflicting sidecars are reported without silently attaching data. An incomplete/corrupt recording is preserved and reported while other valid recordings remain loadable. Active recordings from an unclean process death become `interrupted`; pending segments become explicit gaps.

New archive types in `internal/domain` are separate from protocol types. Existing JSON field names are retained; epoch, ordinal, provenance, and URI classification are optional additions, so recordings without them remain readable. Adapter ID/version/protocol version/fingerprint are provenance only. Credentials, headers, and adapter state are never copied into recording metadata. Source URLs and raw manifest snapshots are preserved as canonical source data and may contain signed credentials; they are therefore treated as sensitive, stored under restricted recording-directory permissions, and removed from public list/detail projections.

## Management API and projections

`internal/recordquery` filters, searches, sorts, cursor-pages, and computes size/segment/gap statistics from canonical recording snapshots. `/api/v2/recordings` and the dashboard read those snapshots without mutating them. Gap duration is `null` when the archive lacks enough timing data. Tags, workflow history, recording event projections, audit, notifications, and adapter enable preferences live in a separate bounded JSON store under `internal/management`; canonical recordings remain understandable without it.

Storage APIs expose filesystem statistics and an archive index. Delete is allowed only for inactive recordings. The server's per-recording lock serializes deletion against playback and export input reads; active export/integrity jobs prevent deletion. Completed export artifacts are independent projections and remain available until their export job is deleted. Storage first renames the recording directory to a tombstone and then removes it; startup retries interrupted tombstone deletion. The archive index lists only objects referenced by canonical metadata and never exposes absolute paths or source URIs.

Integrity verification is a bounded job service that streams canonical SHA-256/size checks against a recording metadata snapshot. Active recordings are rejected; duplicate jobs for one recording coalesce. Job/results persist separately, and queued/running work found after a process restart is marked interrupted/failed. Verification does not modify or block segment acquisition.

Resource browsing is available only to adapters declaring the generic `resource_browse` capability. Core passes list/search items, cursors, and attributes as opaque data. Queries and result pages are bounded and each call has a timeout. Adapter enable/disable preferences persist separately and are applied after startup discovery. Manual restart repeats the descriptor fingerprint check and cancels only workflows for that adapter; recording workers already using resolved media remain independent.

The dashboard, paginated recording query, tags, delete, archive index, events, integrity, workflow list/history, resource browse/search, global search, notifications, system-info, storage, and bounded request-log APIs use real archive or process data. `/api/logs` is an in-memory activity log containing HTTP method, status, duration, and a safe component label; it does not read OS/application log files and omits URL, query, headers, and bodies. It is cleared on process restart. Global resource search covers only resource chains already known from recordings and does not query external platforms. Notification/workflow/audit/event stores have bounded retention. Current recording event projections cover recording state, observed manifests, gaps, and management job transitions; the Core does not emit an unbounded event for every segment. Integrity verification can be canceled with `POST /api/integrity/jobs/{job_id}/cancel`.

## Authentication, settings, and derived exports

Single-administrator authentication is enabled by default. First-run setup uses the mode-`0600` token at `DATA_DIR/security/bootstrap-token` and a password of at least 12 bytes; only a bcrypt hash is stored. Session tokens come from a cryptographic RNG and are kept in process memory under SHA-256 keys, so a Core restart revokes every session. Browser cookies are HttpOnly and SameSite=Strict, and mutations require a CSRF header. Set `COOKIE_SECURE=1` behind a TLS reverse proxy. `AUTH_DISABLED=1` is accepted only for loopback binds. There is no user/role system, password reset, or external identity provider.

`internal/systemsettings` manages the UI theme (immediate), integrity concurrency (persisted but applied after server restart), and an optional retention policy. Retention defaults to disabled with a 30-day age threshold. When enabled, only completed recordings older than the configured threshold are eligible; any tagged recording is protected, as are recordings with active integrity or derivative jobs. The server performs one bounded pass at startup and then every 24 hours, deleting at most 100 recordings per pass. `GET /api/retention/candidates` previews candidates and `POST /api/retention/run` explicitly runs a pass. Bind address, storage root, and adapter directories are read-only. Settings use strict validation and atomic private-file replacement.

The optional `internal/derivative` service activates only when an FFmpeg executable is available. Export validates and privately copies source HLS payloads into a separate staging directory, creates a local playlist, and performs MKV `-c copy` remuxing for stopped/completed recordings. It passes a structured argument vector without a shell and disallows network protocols. Jobs, cancellation, timeouts, restart recovery, and downloads live outside the canonical archive; the recording directory and metadata are not changed. Thumbnail generation reads the first archived video frame into a bounded, private JPEG projection under `thumbnails/`; it uses verified local payload copies and never fetches the source URI. `GET /api/recordings/{id}/thumbnail` serves the projection and `POST .../thumbnail/regenerate` replaces it. A generation failure does not change the archive. When FFmpeg is missing, both export and thumbnail generation are unavailable and the UI shows the placeholder. Transcoding and additional containers are not implemented.

Recording directories are mode `0700`; metadata, payload, and sidecar files use mode `0600`. File secret/state backends do not encrypt values at rest. The management API provides one administrator but no multi-user/role authorization. Local runs bind to loopback by default and Compose publishes only on `127.0.0.1`. Use TLS and Secure cookies behind a reverse proxy, and do not expose the control plane directly to untrusted networks. The page bundles hls.js 1.6.7 (Apache-2.0) and retains restrictive CSP/security headers. Scripts remain same-origin only; `style-src-attr` narrowly permits the inline style attributes Radix needs to position popovers and selects.

## Server lifecycle and Docker

On SIGINT/SIGTERM, Core stops accepting HTTP work, cancels all recording workers before waiting for any worker, durably records their terminal states, then shuts down adapter processes. Shutdown is bounded. Compose allows 45 seconds for graceful termination, longer than the configured HTTP and recording-worker shutdown deadlines. A real crash still causes active recordings to reload as interrupted.

The container runs as UID 10001. Compose uses a named `/data` volume and publishes the control port on host loopback. The image contains the Owncast adapter in `/adapters`; mount additional executable adapters read-only at `./adapter-binaries` (container path `/external-adapters`). Set their executable bit before starting Compose. Core must restart to discover additions; there is no hot reload. Even when authentication is explicitly disabled, only loopback binds are allowed.

## Playback and intentionally unimplemented work

For a stopped, completed, or interrupted recording, Core creates a finite seekable HLS VOD manifest over stored source segments. Segment endpoints return stored bytes directly; playback does not concatenate or remux files. Browser codec support remains necessary.

Not implemented: chat/metadata timeline, asynchronous adapter notification runtime, real platform authentication flows, additional platform adapters, workflow persistence across Core restart, encrypted HLS, external rendition synchronization, DASH, archive finalization/TAR, LTO, export transcoding/additional formats, multi-user/role authorization, adapter sandboxing, and adapter hot reload.

## Web application

The management UI is a React 19 + TypeScript SPA under `web/`. It uses Vite, Tailwind, shadcn-style Radix UI components, TanStack Query/Router/Table, Lucide, and hls.js. API requests and CSRF handling live in a shared client; TanStack Query owns server state. The browser uses same-origin `/api` endpoints, and the player bundles hls.js locally.

For local development, run the Go API and Vite in separate terminals:

```sh
go run ./cmd/archiver
npm --prefix web ci
npm --prefix web run dev
```

Production Go binaries embed Vite output under `internal/server/static/ui/`. `make build` builds the frontend before Go; the Dockerfile uses a separate Node build stage. Only allowlisted client routes receive the SPA entry on direct navigation. `/api/**` and `/static/**` are excluded from SPA fallback.

On first start, the UI checks `/api/auth/session` and displays bootstrap or login. The bootstrap token is stored at `DATA_DIR/security/bootstrap-token` and is submitted with the password to `/api/auth/bootstrap`. After login, the shared API client automatically sends the session CSRF token on mutation requests.
