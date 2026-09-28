# Integrated Recorder

[한국어](README.md) | **English**

A headless, segment-native live-stream archival server written in Go.

Integrated Recorder follows the source manifest, stores original media segments without transcoding or remuxing, and reconstructs them as browser-playable VOD. The recording path does **not** depend on FFmpeg or Streamlink.

> [!NOTE]
> Integrated Recorder is the **spiritual successor** to [Twitch Auto Recorder](https://github.com/dltkddnr04/Twitch-Auto-Recorder) and [AfreecaTV Auto Recorder](https://github.com/dltkddnr04/AfreecaTV-Auto-Recorder).
>
> Those projects focused on automatically detecting live broadcasts and saving them locally, using platform-specific discovery with Streamlink/FFmpeg-based recording. Integrated Recorder keeps the same goal of unattended stream archiving, but is a ground-up redesign around a headless Go service, direct segment preservation, browser control, and long-term archival.

## Principles

- **Preserve the source:** store original media payloads unchanged.
- **Segment-native:** do not turn every recording into one monolithic file during acquisition.
- **Headless first:** designed to run continuously in Docker.
- **Browser controlled:** the API and web interface are the intended control surface.
- **Rebuildable projections:** VOD playlists, preview indexes, exports, and future UI data should be reproducible from the archive.
- **FFmpeg is for derivatives only:** it is not required for acquisition or canonical archival. It is used only for optional projections such as preview frames and remux exports.

See [Architecture](docs/ARCHITECTURE.md) for the detailed design and storage direction.

Adapters declare input/settings schemas and may discover opaque resources or suspend for generic configuration challenges. Core must restart to discover newly installed adapter binaries. Installed adapters run as trusted local code. The default file secret stores use restricted permissions but do not encrypt values at rest.

## Current status

**Milestone 1 and the external adapter protocol milestone are complete.**

Management UI v2 connects recording search/pagination, tags/deletion, integrity checks and cancellation, adapter controls, capability-driven resource browsing, workflows, notifications, supported settings and optional recording retention, global search, request-log viewing, and the Preview Frame Index to backend APIs. First-run administrator setup uses `<DATA_DIR>/security/bootstrap-token`. If FFmpeg is available, segment preview generation and separate remux exports are offered without changing the canonical recording.

Scene previews are an opt-in derivative per recording and default to disabled. A background service produces at most one reusable frame for each committed primary-track segment. It first tries the target segment alone (including the required fMP4 init object); only after decode failure does it stage bounded prior-segment context. Posters, storyboards, and future navigation views reuse the stored frames, and slow or failed FFmpeg work never blocks acquisition.

Currently supported:

- external executable adapters, with a platform-agnostic Core and Owncast as the first adapter;
- schema-rendered input and settings forms with generic resource discovery and configuration challenge/resume;
- hierarchical settings with separate stored and effective projections;
- lazy adapter-process restart with bounded backoff and a fresh describe handshake;
- adapter-declared refresh for expiring media sources;
- a bounded single-rendition HLS subset, including complete segments in LL-HLS playlists;
- direct acquisition of MPEG-TS/fMP4-style source objects;
- SHA-256 and size metadata for captured payloads;
- manifest snapshots;
- duplicate suppression, bounded retries, and gap detection;
- restart-safe recording metadata;
- generated finite HLS VOD playback;
- browser playback and seek.

The first live acceptance test used the public Owncast TV example stream: 90 segments, about 270 seconds of VOD, zero detected gaps, successful restart/reload, and successful seeks at 0:00, 2:15, and 4:27. Stored segment hashes matched re-fetched source objects.

Not supported yet:

- chat and broadcast-metadata timeline;
- finalized TAR/index archive format;
- HDD/NAS/LTO storage lifecycle;
- additional platform adapters;
- external audio rendition synchronization;
- encrypted HLS and partial-only/delta LL-HLS;
- DRM workflows;
- multi-user and role-based authorization;
- transcoding or additional export formats.

## Run

Requires Go 1.23+.

```sh
mkdir -p adapters
go build -o adapters/integrated-recorder-adapter-owncast ./cmd/adapters/owncast
DATA_DIR=./data ADAPTER_DIR=./adapters ADDR=127.0.0.1:8080 go run ./cmd/archiver
```

Then open `http://localhost:8080/`.

Docker configuration is included. The standard runtime image includes Alpine Linux's `ffmpeg` package for scene previews and MKV remux derivatives. Host installations do not require FFmpeg; canonical recording and VOD playback work without it. Alpine v3.21 package metadata identifies the `ffmpeg` license expression as `GPL-2.0-or-later AND LGPL-2.1-or-later`. FFmpeg upstream notes that optional GPL-covered components can affect distribution licensing. Before redistribution, check the exact image package metadata and the [Alpine package record](https://pkgs.alpinelinux.org/package/v3.21/community/x86/ffmpeg) and [FFmpeg legal considerations](https://ffmpeg.org/legal.html).

```sh
docker compose up --build
```

The container uses a named `/data` volume and publishes the control API on host loopback. The image includes Owncast under `/adapters`; place extra executable adapters in `./adapter-binaries`, mounted read-only at `/external-adapters`, then restart Core to discover them. The unauthenticated control API is intended for a trusted host/private network or an authenticated reverse proxy; do not expose it directly to untrusted networks.

## API

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Health check |
| `GET` | `/api/adapters` | Available adapters and status |
| `GET` | `/api/adapters/{id}/schema` | Adapter input and settings schema |
| `GET` / `PUT` | `/api/adapters/{id}/config` | Plugin-defined settings; secret values are never returned |
| `POST` | `/api/recordings` | Start recording |
| `GET` | `/api/resolve-workflows/{id}` | Inspect a suspended resource/configuration workflow |
| `POST` | `/api/resolve-workflows/{id}/continue` | Submit answers and resume a workflow |
| `DELETE` | `/api/resolve-workflows/{id}` | Cancel a suspended workflow |
| `GET` | `/api/recordings` | List recordings |
| `GET` | `/api/v2/recordings` | Search, filter, sort, and cursor-page recordings |
| `GET` | `/api/dashboard` | Real recording, storage, integrity, and adapter status |
| `GET` / `PUT` | `/api/recordings/{id}/tags` | Manage tags |
| `DELETE` | `/api/recordings/{id}` | Delete an inactive recording |
| `POST` | `/api/recordings/{id}/integrity/verify` | Start asynchronous archive verification |
| `POST` | `/api/integrity/jobs/{job_id}/cancel` | Cancel an active integrity verification |
| `GET` | `/api/logs` | Query the bounded application request log |
| `GET` | `/api/recordings/{id}/archive/index` | List canonical archive objects |
| `GET` | `/api/recordings/{id}/previews` | Get a bounded sample from the Preview Frame Index |
| `GET` | `/api/recordings/{id}/previews/{archive_ordinal}` | Get an individual scene preview frame |
| `POST` | `/api/recordings/{id}/previews` | Enable or reconcile per-segment preview generation |
| `GET` | `/api/adapters/{id}/resources` | List resources when the adapter advertises browse capability |
| `POST` | `/api/adapters/{id}/restart`, `/enable`, `/disable` | Manage discovered adapter processes |
| `POST` | `/api/recordings/{id}/exports` | Request MKV remux when FFmpeg is available |
| `GET` | `/api/recordings/{id}/thumbnail` | Read a compatibility poster projection from the Preview Frame Index |
| `POST` | `/api/recordings/{id}/thumbnail/regenerate` | Compatibility endpoint to request preview generation/reconciliation |
| `GET` / `PUT` | `/api/settings` | Supported UI theme and integrity concurrency settings |
| `POST` | `/api/auth/login`, `/logout`, `/bootstrap` | Single-administrator session authentication |
| `GET` | `/api/recordings/{id}` | Recording details |
| `POST` | `/api/recordings/{id}/stop` | Stop recording |
| `GET` | `/api/recordings/{id}/play/master.m3u8` | Generated VOD master playlist |
| `GET` | `/api/recordings/{id}/play/tracks/{track}/playlist.m3u8` | Generated VOD media playlist |
| `GET` | `/api/recordings/{id}/play/segments/{segmentID}` | Original stored payload |

Start a recording with the first adapter:

```sh
curl -X POST http://localhost:8080/api/recordings \
  -H 'Content-Type: application/json' \
  -d '{"adapter_id":"owncast","input":{"source_url":"https://watch.owncast.online"},"title":"optional title","preview_mode":"segment"}'
```

`preview_mode` is optional and defaults to `disabled`. Setting it to `segment` schedules scene previews asynchronously after canonical segments are committed.

## Development

Web UI development requires Node.js 20 or newer. Run the Go API and Vite development server in separate terminals.

```sh
go run ./cmd/archiver
npm --prefix web ci
npm --prefix web run dev
```

Build the React assets into the Go embed directory before building a production binary. `make build` runs the UI build followed by the Go build.

```sh
npm --prefix web run build
go build ./...
# or
make build
```

For first-run administrator setup, read the token from `<DATA_DIR>/security/bootstrap-token`.

Validation commands:

```sh
go test -race -count=1 ./...
go vet ./...
```

## Roadmap

- [x] Original segment acquisition + restart-safe VOD playback
- [x] Platform-agnostic Core + external Adapter Protocol v1 + Owncast binary
- [x] Resource discovery, configuration inheritance, and challenge/resume foundation
- [ ] Broadcast metadata + chat timeline
- [ ] Finalized archive packaging + random-access index
- [x] React management SPA and connected product API foundation
- [ ] Additional platform adapters such as CHZZK, SOOP, and Twitch
- [ ] Hot/cold storage lifecycle, including HDD/NAS/LTO
- [x] Optional segment-based Preview Frame Index and remux-only export (when FFmpeg is available)

## Documentation

- [Architecture](docs/ARCHITECTURE.md)

## License

GNU Affero General Public License v3.0 only (**AGPL-3.0-only**). See [LICENSE](LICENSE).
