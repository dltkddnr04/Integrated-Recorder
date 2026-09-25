# Integrated Recorder Architecture

[README](../README.md) | [한국어](ARCHITECTURE.ko.md)

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
                       Browser
                          │
                    HTTP API / VOD
                          │
                  ┌───────▼───────┐
                  │ Control/Serve │
                  └───────┬───────┘
                          │
             ┌────────────┴────────────┐
             │                         │
      Platform discovery        Playback projection
             │                         │
             ▼                         │
        HLS acquisition                │
             │                         │
             ▼                         │
      Original segments ───────────────┘
      + manifest snapshots
      + recording metadata
             │
             ▼
        Working storage
```

Current package boundaries:

- `internal/platform/owncast` — platform-specific discovery only.
- `internal/hls` — HLS parsing and rendition selection.
- `internal/acquire` — recording lifecycle, polling, retries, deduplication, acquisition, and gap detection.
- `internal/storage` — recording directories, sidecars, manifest snapshots, and payload access.
- `internal/network` — bounded HTTP client and source-address validation.
- `internal/server` — control API, minimal browser page, VOD playlist generation, and segment serving.
- `cmd/archiver` — process wiring and HTTP lifecycle.

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
