# Integrated Recorder

A small headless Go server that archives one Owncast HLS rendition as original
segment files and serves a finite VOD HLS playlist after recording stops.
Recordings are directories containing their own JSON metadata and source media;
there is no application database.

## Run locally

Requires Go 1.23 or newer.

```sh
DATA_DIR=./data ADDR=:8080 go run ./cmd/archiver
```

Or run in Docker with persistent storage:

```sh
docker compose up --build
```

The default local data path is `./data`. The container sets `DATA_DIR=/data`
and mounts `./data:/data`. `ADDR` and `DATA_DIR` can be changed through the
environment. The service has no interactive CLI.

## API

- `GET /healthz` — liveness check.
- `POST /api/recordings` — start a recording with
  `{"source_url":"https://owncast.example","title":"optional"}`.
  `source_url` is the Owncast instance base URL, not a manifest URL.
- `GET /api/recordings` — list active and previously saved recordings.
- `GET /api/recordings/{id}` — recording metadata, counts, duration, and gaps.
- `POST /api/recordings/{id}/stop` — stop polling and wait for state to persist.
- `GET /api/recordings/{id}/play/master.m3u8` — generated VOD master playlist.
- `GET /api/recordings/{id}/play/tracks/main/playlist.m3u8` — generated finite
  VOD timeline.
- `GET /api/recordings/{id}/play/segments/{segmentID}` — original stored init or
  media payload bytes.

Playback endpoints become available after a recording is stopped, completed,
or interrupted. Active recordings are not advertised as final VOD timelines.

## Current format support

The Owncast resolver uses the documented `/hls/stream.m3u8` entry point. If it
is a master playlist, the server selects the highest-bandwidth rendition that
does not require an external audio group. One muxed rendition is captured.
The shared parser supports ordinary HLS media playlists with media sequence,
target duration, `EXTINF`, program date/time, `EXT-X-MAP`, byte ranges,
discontinuities, and `ENDLIST`. Common MPEG-TS and fMP4 payloads are stored as
the server returns them. A required byte range must be served with a matching
HTTP 206 response. Encryption/DRM, external audio groups, LL-HLS parts, and
alternate rendition synchronization are unsupported and rejected or not
selected. Live snapshots are kept under each recording's `manifests/` folder.

The working directory layout is self-describing, for example:

```text
data/recordings/<id>/recording.json
data/recordings/<id>/manifests/*.m3u8
data/recordings/<id>/tracks/main/<payload files and adjacent .json sidecars>
```

Payload hashes and sizes are calculated while the raw HTTP response body is
written to a temporary file, then the file is atomically published. The service
does not decode, encode, concatenate, remux, or transform media. It does not
invoke or depend on FFmpeg or Streamlink. There is no database requirement;
metadata and media files are enough to reload and play a recording after a
server restart. A process restart changes any stale `recording` state to
`interrupted` while retaining captured data.

The source URL is user-provided, so the service rejects localhost/private
addresses and validates resolved dial addresses to reduce SSRF risk. DNS is
resolved again for each connection and validated addresses are dialed directly;
DNS and routing behavior on the host network remain an operational boundary.

## Playback notes

Safari can use native HLS. The plain control page at `/` uses native playback
when available and falls back to hls.js 1.5.17 from jsDelivr for browsers such
as Chrome and Firefox. The fallback requires access to that CDN. Browsers vary
in their support for codecs used by a particular broadcaster; the server keeps
the source bytes and does not make incompatible media browser-compatible.

## Tests and live-stream checks

Run deterministic parser, resolver, storage, and local HTTP acquisition tests:

```sh
go test ./...
go vet ./...
```

The tests use local fixtures and verify byte identity, duplicate suppression,
reload, and VOD playlist generation. They do not connect to an internet stream.
Before relying on a broadcaster, start a recording through the API, leave it
running for several minutes, stop it, restart the process, and test seek/play in
the target browser. No live platform E2E test is claimed by this repository.
