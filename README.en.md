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
- **Rebuildable projections:** VOD playlists, indexes, exports, and future UI data should be reproducible from the archive.
- **FFmpeg is optional:** it may be used later for explicit export/transcoding, not for recording.

See [Architecture](docs/ARCHITECTURE.md) for the detailed design and storage direction.

Adapters declare input/settings schemas and may discover opaque resources or suspend for generic configuration challenges. Core must restart to discover newly installed adapter binaries. The default file secret store uses restricted permissions but does not encrypt values at rest.

## Current status

**Milestone 1 and the external adapter protocol milestone are complete.**

Currently supported:

- external executable adapters, with a platform-agnostic Core and Owncast as the first adapter;
- schema-rendered input and settings forms with generic resource discovery and configuration challenge/resume;
- hierarchical settings with separate stored and effective projections;
- lazy adapter-process restart with bounded backoff and a fresh describe handshake;
- ordinary HLS master/media playlists;
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
- LL-HLS/delta playlists;
- DRM workflows;
- authentication/authorization;
- export/transcoding.

## Run

Requires Go 1.23+.

```sh
mkdir -p adapters
go build -o adapters/integrated-recorder-adapter-owncast ./cmd/adapters/owncast
DATA_DIR=./data ADAPTER_DIR=./adapters ADDR=:8080 go run ./cmd/archiver
```

Then open `http://localhost:8080/`.

Docker configuration is included:

```sh
docker compose up --build
```

The container uses `/data` for persistent recording storage. The Docker image/runtime path has not yet been validated on a live Docker daemon.

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
| `GET` | `/api/recordings` | List recordings |
| `GET` | `/api/recordings/{id}` | Recording details |
| `POST` | `/api/recordings/{id}/stop` | Stop recording |
| `GET` | `/api/recordings/{id}/play/master.m3u8` | Generated VOD master playlist |
| `GET` | `/api/recordings/{id}/play/tracks/{track}/playlist.m3u8` | Generated VOD media playlist |
| `GET` | `/api/recordings/{id}/play/segments/{segmentID}` | Original stored payload |

Start a recording with the first adapter:

```sh
curl -X POST http://localhost:8080/api/recordings \
  -H 'Content-Type: application/json' \
  -d '{"adapter_id":"owncast","input":{"source_url":"https://watch.owncast.online"},"title":"optional title"}'
```

## Development

```sh
go test -race -count=1 ./...
go vet ./...
go build ./...
```

## Roadmap

- [x] Original segment acquisition + restart-safe VOD playback
- [x] Platform-agnostic Core + external Adapter Protocol v1 + Owncast binary
- [x] Resource discovery, configuration inheritance, and challenge/resume foundation
- [ ] Broadcast metadata + chat timeline
- [ ] Finalized archive packaging + random-access index
- [ ] Full browser UI
- [ ] Additional platform adapters such as CHZZK, SOOP, and Twitch
- [ ] Hot/cold storage lifecycle, including HDD/NAS/LTO
- [ ] Optional export pipeline

## Documentation

- [Architecture](docs/ARCHITECTURE.md)

## License

GNU Affero General Public License v3.0 only (**AGPL-3.0-only**). See [LICENSE](LICENSE).
