# Integrated Recorder

**Integrated Recorder** is a headless, segment-native live-stream archival server written in Go.

The project is built around a simple idea:

> Preserve the source stream first. Everything else — browser playback, search indexes, exports, thumbnails, and future UI views — should be reproducible projections of that preserved source data.

Integrated Recorder does **not** use FFmpeg or Streamlink in the recording path. Instead, it follows the source manifest, downloads the original media objects directly, preserves their bytes unchanged, records the metadata needed to reconstruct the timeline, and later serves the result back to a browser as VOD.

The long-term target is a self-hosted archival system that runs headlessly in Docker, is fully controlled from a browser, preserves broadcast metadata and chat alongside media, and can move finalized archives across SSD/HDD/NAS/LTO storage without changing the canonical recording.

> [!IMPORTANT]
> The project is in early development. Milestone 1 — original-segment acquisition, restart-safe persistence, and browser VOD reconstruction — is working and has been validated end-to-end. Many of the long-term features described below are architectural direction, not current functionality.

## Why segment-native?

Most "recorders" treat the desired output file as the primary artifact:

```text
live stream
    ↓
recorder / transcoder
    ↓
single media file
```

Integrated Recorder treats the **stream objects themselves** as the primary artifact:

```text
live stream
    ↓
manifest tracking
    ↓
original media segments + timing metadata
    ↓
canonical recording
    ├── browser VOD
    ├── future chat replay
    ├── future archive packaging
    └── optional future export/transcode
```

This keeps the acquisition path simple and lossless:

- no decode/encode cycle;
- no transcoding during recording;
- no remux requirement during recording;
- no dependency on FFmpeg or Streamlink for acquisition;
- original payload bytes are preserved;
- segment hashes can be used to verify integrity;
- playback can be reconstructed without first creating a monolithic media file.

FFmpeg may eventually be supported as an **optional export backend** when a user explicitly asks for a conventional MP4/MKV or a real transcode. It is not part of the recorder core.

## Current status

### Milestone 1 — complete

The current implementation can:

- run as a headless Go HTTP server;
- resolve an Owncast instance to its documented HLS entry point;
- parse a master/media HLS playlist;
- choose the highest-bandwidth muxed rendition that does not require an external audio group;
- poll a sliding live playlist;
- detect new media sequences;
- suppress duplicate acquisition;
- retry bounded network failures;
- detect skipped sequence ranges and explicit `EXT-X-GAP` entries;
- preserve init/media payload bytes directly to disk;
- record payload size and SHA-256;
- store manifest snapshots with hashes;
- persist a self-describing `recording.json`;
- reload recordings after a process restart;
- convert stale in-progress state to `interrupted` without discarding captured data;
- generate a finite HLS VOD projection from the stored segments;
- serve original stored segment bytes back over HTTP;
- play and seek the reconstructed VOD in a browser.

There is deliberately **no application database** in Milestone 1. The recording directory itself contains the information required to reload and serve the recording.

### Current platform support

Only **Owncast** is currently implemented as a platform adapter.

Owncast is useful at this stage because its HLS entry point is documented and predictable. It lets the project validate the recorder core independently from platform-specific reverse engineering. Owncast is a validation target, not the intended limit of the project.

The current adapter resolves an Owncast instance base URL to:

```text
/hls/stream.m3u8
```

The shared HLS layer, not the Owncast adapter, owns playlist parsing, rendition selection, segment acquisition, integrity metadata, and VOD reconstruction.

### Current HLS scope

Supported today:

- ordinary HLS master/media playlists;
- media sequence;
- target duration;
- `EXTINF`;
- `PROGRAM-DATE-TIME`;
- `EXT-X-MAP`;
- byte ranges;
- discontinuities;
- `EXT-X-GAP`;
- `ENDLIST`;
- MPEG-TS and fMP4-style source payload storage.

Not supported yet:

- DRM/encrypted playback workflows;
- external audio rendition synchronization;
- LL-HLS parts;
- delta playlists;
- alternate rendition synchronization;
- multi-track archival beyond the current single selected muxed rendition.

## End-to-end validation

Milestone 1 was tested against the public Owncast TV example stream.

The acceptance run recorded approximately four minutes of a real live stream:

- **90 media segments** captured;
- approximately **270 seconds** of reconstructed VOD timeline;
- **0 detected gaps**;
- stored segment size and SHA-256 matched the corresponding source object when re-fetched;
- the process was stopped and restarted using the same data directory;
- the recording was discovered again after restart;
- browser playback succeeded;
- seeks were tested at **0:00**, **2:15**, and **4:27**;
- playback endpoints returned the original stored segment bytes;
- no FFmpeg or Streamlink process or dependency was used.

The live test also exposed a real-world parser compatibility issue: Owncast emitted `PROGRAM-DATE-TIME` offsets in `+0000` form. The parser was updated to accept that form and a regression test was added.

Automated checks currently pass with:

```sh
go test -race -count=1 ./...
go vet ./...
go build ./...
```

Docker configuration is present, but an actual image build/runtime test has not yet been completed because Docker was unavailable during the initial acceptance run.

## Architecture

The current code is intentionally small, but its boundaries are already aligned with the longer-term system.

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

Current package responsibilities:

- `internal/platform/owncast` — Owncast-specific stream discovery only;
- `internal/hls` — HLS parsing and rendition selection;
- `internal/acquire` — recording lifecycle, live playlist polling, segment acquisition, retries, deduplication, and gap detection;
- `internal/storage` — self-describing recording directories, sidecars, manifests, and payload access;
- `internal/network` — bounded HTTP client and public-address validation;
- `internal/server` — control API, minimal browser page, generated VOD playlists, and segment serving;
- `cmd/archiver` — process wiring and HTTP server lifecycle.

A key architectural rule is that **platform-specific discovery should stay above the recorder core**. Adding CHZZK, SOOP, Twitch, or another service should not require teaching the HLS acquisition engine about that service's URL conventions or API semantics.

## Canonical data vs projections

The long-term model is:

```text
Canonical data
├── original media objects
├── source/timing metadata
├── manifest history
├── future broadcast metadata
└── future chat/events

Derived projections
├── browser HLS VOD
├── database/search index
├── thumbnails
├── chat replay UI
├── conventional exported files
└── other presentation formats
```

A projection should be disposable. Losing an index or rebuilding the application should not make an intact archive undecodable.

This is also why long-term archive formats should remain self-describing and recoverable without a private database schema.

## Recording layout

A recording currently resembles:

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

The media payload is written without decoding or transformation. Metadata is written separately so the source object remains independently verifiable.

For each captured media object, the recording model can retain information such as:

- track ID;
- sequence number;
- source URI;
- duration;
- program date/time when available;
- init-segment reference;
- byte range;
- discontinuity state;
- storage path;
- payload size;
- SHA-256.

Manifest snapshots are also stored with size, SHA-256, fetch time, and source URI.

## Browser playback

A completed/interrupted recording is exposed as a generated HLS VOD rather than being concatenated into a new media file.

Conceptually:

```text
recording metadata
      +
stored segments
      ↓
generated VOD playlist
      ↓
browser requests a segment
      ↓
server returns preserved source bytes
```

This lets the browser seek through the archived timeline while keeping the stored representation segment-native.

Safari can use native HLS support. The current minimal page falls back to hls.js 1.5.17 from jsDelivr for browsers without native HLS support.

Codec compatibility is still determined by the source media and the browser. Integrated Recorder does not transcode incompatible media merely to make it playable.

## HTTP API

Current endpoints:

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness check |
| `POST` | `/api/recordings` | Start a recording |
| `GET` | `/api/recordings` | List recordings |
| `GET` | `/api/recordings/{id}` | Recording details |
| `POST` | `/api/recordings/{id}/stop` | Stop an active recording |
| `GET` | `/api/recordings/{id}/play/master.m3u8` | Generated VOD master playlist |
| `GET` | `/api/recordings/{id}/play/tracks/{track}/playlist.m3u8` | Generated VOD media playlist |
| `GET` | `/api/recordings/{id}/play/segments/{segmentID}` | Original stored payload |

Start an Owncast recording:

```sh
curl -X POST http://localhost:8080/api/recordings \
  -H 'Content-Type: application/json' \
  -d '{"source_url":"https://watch.owncast.online","title":"optional title"}'
```

`source_url` is currently expected to be an Owncast **instance base URL**, not a direct manifest URL.

## Running

### Local

Requires Go 1.23 or newer.

```sh
DATA_DIR=./data ADDR=:8080 go run ./cmd/archiver
```

Then open:

```text
http://localhost:8080/
```

The page is intentionally minimal. It exists to exercise the headless API and playback path, not as the final product UI.

### Docker

```sh
docker compose up --build
```

The provided Compose configuration:

- exposes port `8080`;
- sets `DATA_DIR=/data`;
- mounts `./data:/data`;
- restarts the service unless stopped.

The intended deployment model is a long-running headless container with persistent archive storage mounted into it.

## Network security

The current API accepts a user-provided source URL, so the default network client rejects localhost, private, loopback, link-local, multicast, and unspecified source addresses and re-validates resolved dial targets.

That is a security default, not a permanent statement that local/self-hosted sources will never be supported. A future configuration may permit explicitly trusted private-network sources without weakening the default behavior.

Do not expose the current control API directly to an untrusted network. Authentication and authorization are not implemented yet.

## Long-term archive direction

The working-directory representation is intentionally separate from the final cold-storage representation.

The expected direction is:

```text
live acquisition
      ↓
working recording directory
      ↓
finalize
      ↓
archive package + random-access index
      ↓
HDD / NAS / LTO
```

TAR is a strong candidate for finalized archival packaging because:

- it is simple and widely recoverable;
- it maps naturally to sequential media such as LTO;
- it avoids scattering huge numbers of small segment files across cold storage;
- the media is already compressed, so whole-archive compression is usually of limited value;
- an external/sidecar index can provide efficient byte-offset access on seekable storage without making that index canonical.

The intended principle is:

> The archive is canonical; the index is an acceleration structure that should be rebuildable.

This format is **not implemented yet**.

## Roadmap

The near-term direction is intentionally incremental:

1. **Original segment acquisition + restart-safe VOD playback** — complete.
2. **Broadcast metadata and chat timeline** — preserve title/category/state changes and chat/events on the same recording time axis.
3. **Finalized archive packaging and random-access index** — designed for HDD/NAS/LTO workflows.
4. **Full browser control UI** — all recorder management and VOD playback without a local CLI.
5. **Additional platform adapters** — e.g. CHZZK, SOOP, Twitch, while keeping platform quirks outside the recorder core.
6. **Storage lifecycle/tiering** — move finalized recordings between hot and cold storage.
7. **Optional export pipeline** — remux/transcode only when explicitly requested by the user.

The ordering may change as real platform behavior exposes new requirements.

## Non-goals

At least for the recorder core:

- transcoding every stream while recording;
- requiring FFmpeg or Streamlink for acquisition;
- requiring a central database to understand stored recordings;
- converting every recording to a single monolithic media file immediately;
- embedding platform-specific behavior throughout the acquisition engine.

## License

A license has **not been selected yet**.

The repository is public, but until a license is added, no open-source license is granted. Licensing will be decided explicitly rather than inferred from repository visibility.
