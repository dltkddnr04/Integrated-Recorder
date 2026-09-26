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

Recording directories are mode `0700`; metadata, payload, and sidecar files use mode `0600`. The file backends are not encrypted at rest. The unauthenticated management API is a trusted control plane and has no user/role system. Local runs bind to loopback by default and Compose publishes only on `127.0.0.1`. Do not expose it directly to untrusted networks; use a trusted private network or an authenticated reverse proxy. The page uses local pinned hls.js 1.5.17 (Apache-2.0) and a restrictive CSP/security headers.

## Server lifecycle and Docker

On SIGINT/SIGTERM, Core stops accepting HTTP work, cancels all recording workers before waiting for any worker, durably records their terminal states, then shuts down adapter processes. Shutdown is bounded. Compose allows 45 seconds for graceful termination, longer than the configured HTTP and recording-worker shutdown deadlines. A real crash still causes active recordings to reload as interrupted.

The container runs as UID 10001. Compose uses a named `/data` volume and publishes the control port on host loopback. The image contains the Owncast adapter in `/adapters`; mount additional executable adapters read-only at `./adapter-binaries` (container path `/external-adapters`). Set their executable bit before starting Compose. Core must restart to discover additions; there is no hot reload. `ADDR` should remain private because API authentication is not implemented.

## Playback and intentionally unimplemented work

For a stopped, completed, or interrupted recording, Core creates a finite seekable HLS VOD manifest over stored source segments. Segment endpoints return stored bytes directly; playback does not concatenate or remux files. Browser codec support remains necessary.

Not implemented: chat/metadata timeline, notifications, real platform authentication flows, additional platform adapters, workflow persistence across Core restart, encrypted HLS, external rendition synchronization, DASH, archive finalization/TAR, LTO, export/transcoding, database, authentication/authorization, adapter sandboxing, and adapter hot reload.
