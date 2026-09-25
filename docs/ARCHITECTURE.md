# Integrated Recorder Architecture

[English README](../README.en.md) | [한국어 README](../README.md)

This document records the architectural direction behind Integrated Recorder. It intentionally contains detail that does not belong in the project landing page.

## Design goal

Integrated Recorder is not primarily a "video file downloader." It is a live-stream archival system whose canonical data is the source stream itself: original media objects plus enough metadata to reconstruct the recording later.

The central invariant is:

> Preserve source data first. Treat playback, indexes, exports, and UI views as rebuildable projections.

The recorder therefore does not decode, encode, transcode, or remux media during acquisition. FFmpeg and Streamlink are not dependencies of the recording path.

## Canonical data and projections

Long-term canonical data:

```text
Recording
├── original media objects
├── source/timing metadata
├── manifest history
├── broadcast metadata
└── chat/events
```

Derived projections may include:

```text
browser HLS VOD
database/search indexes
thumbnails
chat replay
MP4/MKV exports
other presentation formats
```

A derived projection should be disposable. An intact archive should remain understandable even if an application database or index is lost.

## Current architecture

```text
 Browser ─── HTTP API / VOD ─── Core
                                  │
                    ┌─────────────┴─────────────┐
                    │                           │
              adapterhost                 HLS acquisition
                    │                           │
            framed JSON over stdio              │
                    │                           ▼
          external adapter binary ── resolve  source manifests
                                                │
                              Original segments + snapshots
                                                │
                                          Working storage
```

Current package boundaries:

- `internal/adapterproto` — newline-delimited JSON Protocol v1 envelopes, schemas, resources, media sources, and interaction types.
- `internal/adapterhost` — scans the configured adapter directory and manages standalone process startup, handshake, requests, and shutdown.
- `internal/pluginconfig` — opaque configuration and a separate secret store; file paths are hashed and files use restrictive permissions.
- `internal/interaction` — minimal generic interaction progress state machine.
- `internal/adapters/owncast` + `cmd/adapters/owncast` — standalone adapter implementation only; Core never imports this package.
- `internal/hls` — shared HLS parsing and rendition selection.
- `internal/acquire` — recording lifecycle, polling, retries, deduplication, acquisition, and gap detection.
- `internal/storage` — recording directories, sidecars, manifest snapshots, and payload access.
- `internal/network` — bounded HTTP client and source-address validation.
- `internal/server` — control API, minimal browser page, VOD playlist generation, and segment serving.
- `cmd/archiver` — adapter discovery, service wiring, and HTTP lifecycle.

## External adapter Protocol v1

An adapter is a standalone executable, not a Go package statically linked into Core. Core scans only `ADAPTER_DIR` for executable regular files named `integrated-recorder-adapter-*`; it never scans all of `PATH`. Adding or replacing an adapter binary does not require rebuilding Core.

IPC uses newline-delimited JSON on stdin/stdout, with one message per line. Existing v1 request/response envelopes retain their untyped wire format: requests carry a protocol version, request ID, and method; responses echo the ID and contain either a result or structured `{code, message, details}` error. For future asynchronous extensions, the protocol reserves `{"protocol_version":1,"type":"notification","method":"...","params":{...}}`; the parser classifies it separately from ID-bearing responses. The current runtime does not deliver notifications and safely treats one received while awaiting a response as a protocol error. Frame size is bounded. Requests to a process are serialized. A timeout, malformed frame, protocol version/ID mismatch, or process exit discards that process. On the next operation Core lazily restarts it with bounded backoff, runs `describe` again, and accepts it only if its adapter ID, version, and compatible protocol identity still match. There is no background restart loop. Stdout is reserved for protocol messages; diagnostics go to stderr. Core validates `describe` first, keeps the process alive, and requests graceful shutdown before terminating it.

The implemented v1 operations are `describe`, `resolve`, `resolve.begin`, `resolve.continue`, and `shutdown`. Adapters that declare the generic `resolve_workflow` capability can receive opaque input before a stable resource is known, discover a resource, and return a challenge or media source. Adapters without that capability keep using `resolve`. `get_status`, `configure`, `interaction.begin`, `interaction.continue`, `metadata`, `events`, and `refresh` remain reserved without active runtime implementations. Unknown operations return a structured `unsupported_method` error.

A descriptor carries adapter ID/name/version, protocol version, opaque capability strings, input and configuration schemas, adapter-declared opaque resource types, and supported media types. Core does not interpret capability values or platform meanings embedded in resource type names. For acquisition, Core currently dispatches only the generic `hls` media type.

## Schemas, resources, configuration, and secrets

Adapter-defined schemas let a generic UI render fields without adapter-specific form code. A field can declare its key, control, label, description, required/default state, constraints, options, inheritance, and opaque `visible_when` value. Controls are `text`, `secret`, `number`, `boolean`, `select`, `multi-select`, `textarea`, `action`, and `status`. Choice controls require options. Core validates input, configuration, and challenge values against the declared schema before forwarding them. Action and status fields are display-only. The same small renderer is used for recording input, plugin/resource configuration, and workflow challenges.

Resources use opaque values such as `{resource_type, resource_id, parent}`, with recursive parent references. The descriptor can declare resource types and parent-type relationships. Core compares a type only to select that adapter's schema scope and keeps references intact. A generic Resource type also carries a display name and opaque attributes. There are no Core enums for `account`, `channel`, or `recording`, and resource IDs are never used as filesystem paths.

Configuration scopes form a generic chain: plugin scope, then each parent resource through the current resource. Core uses the supplied opaque parent links only to identify those scopes; unrelated resource chains do not mix. Effective configuration is composed parent-first. Each field's schema `inherit` flag decides whether a value/secret from an ancestor can flow to descendants; it defaults to false for every control, including secrets. A value stored at the current scope always applies there. API views distinguish values stored at the current scope from effective values and identify the opaque source scope for inherited values. Configuration writes are partial; required effective values may come from an ancestor or a workflow challenge.

Ordinary JSON values and secrets use separate, backend-neutral `ConfigStore` and `SecretStore` interfaces. The local file backend hashes scope identifiers, creates directories with mode `0700`, and writes files with mode `0600`. Secret files are separate from ordinary configuration and have restricted permissions, but **they are plaintext at rest; the current backend does not encrypt them**. Core does not log secret values, and API GET responses expose only per-key `configured` state. Blank secret submissions leave existing values unchanged; explicit `clear_secrets` removes a secret. Resolve sends effective settings/secrets to the adapter process only when needed; recording metadata does not persist input, configuration, or secrets. The interface permits a future encrypted or OS-backed implementation, but none is provided now.

## Generic interaction model

Protocol messages can use `action`, `prompt`, `secret_prompt`, `navigate`, `display`, `status`, `complete`, and `error` types with opaque fields/data. Core's tracker accumulates active progress messages per interaction ID and treats `complete` or `error` as terminal. A workflow may report resource discovery, a configuration/interaction challenge, or a resolved media source. Challenges return an opaque workflow ID and schema to the caller; `POST /api/resolve-workflows/{id}/continue` submits answers. Submitted values may be ephemeral or persisted at the discovered resource (or plugin) scope when the challenge permits persistence. Suspended workflows are held in process memory only and are lost when Core restarts. Authentication providers, browser handoff, CAPTCHA, and OTP implementations remain adapter-owned and are outside this milestone.

## Resolve and shared media acquisition

Recording creation accepts `{adapter_id, input, resource?, title?}`. Core treats adapter input as a JSON object, validates it against the adapter's `input_schema`, and does not store it. For workflow-capable adapters, resource discovery happens after this input is sent; Core then loads configuration for the opaque resource chain and resumes the adapter. An adapter's resolved media source carries a media type, manifest URL, HTTP headers, optional session reference, opaque refresh/metadata values, and optional `request_policy.header_forwarding`. The default is restrictive `same_origin`. An adapter may declare `allowlist` origin URLs for forwarding its supplied headers; the current single origin policy applies to all adapter-supplied headers. Comparisons use exact scheme, case-insensitive hostname, and effective port. Core reevaluates the policy for each media URL and redirect, deleting all adapter-supplied header names at each redirect and reapplying them only when the new URL is allowed. This policy authorizes header forwarding only; it does not grant private-address or SSRF access. Core's existing source validator and safe client's address/dial checks remain independent and in force.

HLS master/media parsing, rendition selection, sequence tracking, gap detection, exact init/media segment downloads, SHA-256, and recording persistence remain in Core's `internal/hls` and `internal/acquire`. An adapter resolves platform-specific input into a common media source and does not parse manifests or handle segments.

Platform-specific behavior should remain above the recorder core. Adding CHZZK, SOOP, Twitch, or another source should not require the HLS acquisition engine to understand that platform's API semantics.

## Recording lifecycle

During acquisition, the project currently uses a self-describing working directory rather than a database-backed opaque format.

```text
data/
└── recordings/
    └── <recording-id>/
        ├── recording.json
        ├── manifests/
        │   └── ...
        └── tracks/
            └── main/
                ├── <source payload>
                ├── <source payload>.json
                └── ...
```

For each captured media object the model can retain:

- track identity;
- source sequence;
- source URI;
- duration;
- program date/time when available;
- init-segment reference;
- byte range;
- discontinuity state;
- storage path;
- payload size;
- SHA-256.

Manifest snapshots are also stored with source URI, fetch time, size, and SHA-256.

New recordings retain adapter ID, adapter version, protocol version, and a descriptor fingerprint as provenance; older recordings without this optional field remain readable. The adapter may classify source URIs as `sensitive` or `public` (default `sensitive`). Runtime fetch URLs and raw manifest snapshots remain unchanged canonical recording data, including any credentials embedded in URLs. Recording directories use mode `0700` and metadata files use `0600`. Recording list/detail API projections omit raw source URLs and manifest URIs while retaining the classification and adapter provenance.

Installed adapter binaries are trusted local code: they run as the Core OS user and are not sandboxed. Core scans the adapter directory at startup, so discovering a newly installed binary requires restarting Core; there is no hot-load.

On restart, the server reloads these files. A stale `recording` state becomes `interrupted` while the captured media remains intact.

## Playback projection

A stopped/completed/interrupted recording is not concatenated into a new media file. The server builds a finite HLS VOD playlist from the stored timeline.

```text
recording metadata
      +
stored source segments
      ↓
generated HLS VOD
      ↓
browser requests segment
      ↓
server returns preserved source bytes
```

This allows seekable browser playback while keeping storage segment-native.

Browser codec support still applies. The recorder does not transcode source media merely to make an otherwise unsupported codec playable.

## Metadata and chat timeline

A future recording should preserve non-media events on the same time axis as video/audio.

Examples:

- title changes;
- category changes;
- broadcast state changes;
- chat messages;
- moderation/system events;
- platform-specific raw events.

Normalized fields should make common playback/search operations possible, while raw platform payloads should be retained where useful so future parsers can recover information that was not normalized originally.

## Finalized archive direction

Working storage and cold-storage representation are intentionally separate.

Expected lifecycle:

```text
live acquisition
      ↓
working recording directory
      ↓
finalize
      ↓
archive package + random-access index
      ↓
SSD / HDD / NAS / LTO
```

TAR is a strong candidate for finalized packaging because it is simple, widely recoverable, and naturally suited to sequential media such as LTO. Media segments are already compressed, so whole-archive compression is generally not the primary goal.

A sidecar index can map logical segment IDs to byte offsets on seekable storage.

The intended rule is:

> The archive is canonical. The index is an acceleration structure and should be rebuildable.

The finalized archive format is not implemented yet.

## Network security

The current HTTP client rejects localhost, private, loopback, link-local, multicast, and unspecified source addresses and validates resolved dial targets.

This is a secure default, not a permanent prohibition on trusted private-network sources. A future configuration may explicitly permit local/self-hosted sources without weakening the default behavior.

The current control API has no authentication or authorization and should not be exposed directly to untrusted networks.

## Milestone 1 validation

Milestone 1 was validated against the public Owncast TV example stream:

- 90 media segments;
- about 270 seconds of reconstructed VOD;
- zero detected gaps;
- stored payload size and SHA-256 matched re-fetched source objects;
- restart/reload succeeded;
- browser playback succeeded;
- seeks at 0:00, 2:15, and 4:27 succeeded;
- no FFmpeg or Streamlink was used.

The live run also exposed a real compatibility issue: Owncast emitted `PROGRAM-DATE-TIME` timezone offsets in `+0000` form. The parser was updated and a regression test was added.
