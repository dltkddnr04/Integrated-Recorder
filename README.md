# Integrated Recorder

**한국어** | [English](README.en.md)

Go로 작성된 헤드리스, segment-native 라이브 스트림 아카이빙 서버입니다.

Integrated Recorder는 source manifest를 직접 추적하고 원본 media segment를 transcoding/remuxing 없이 저장한 뒤, 이를 브라우저에서 재생 가능한 VOD로 재구성합니다. 녹화 경로는 **FFmpeg나 Streamlink에 의존하지 않습니다.**

> [!NOTE]
> Integrated Recorder는 [Twitch Auto Recorder](https://github.com/dltkddnr04/Twitch-Auto-Recorder)와 [AfreecaTV Auto Recorder](https://github.com/dltkddnr04/AfreecaTV-Auto-Recorder)의 **정신적 후계자**입니다.
>
> 앞선 프로젝트들은 플랫폼별 방송 감지와 Streamlink/FFmpeg 기반 로컬 자동 녹화를 목표로 했습니다. Integrated Recorder는 “사람이 계속 지켜보지 않아도 라이브 방송을 자동으로 보존한다”는 목표를 이어가되, headless Go 서비스, 원본 segment 직접 보존, 브라우저 제어, 장기 아카이빙을 중심으로 처음부터 다시 설계한 프로젝트입니다.

## 핵심 원칙

- **원본 우선:** media payload를 변형하지 않고 그대로 보존합니다.
- **Segment-native:** acquisition 중 모든 녹화를 하나의 거대한 파일로 만들지 않습니다.
- **Headless first:** Docker에서 장시간 상시 실행하는 것을 전제로 합니다.
- **Browser controlled:** API와 Web UI가 공식 제어 경로가 됩니다.
- **재생성 가능한 projection:** VOD playlist, index, export, 향후 UI 데이터는 archive에서 다시 만들 수 있어야 합니다.
- **FFmpeg는 선택 사항:** 향후 사용자가 export/transcode를 명시적으로 요청할 때만 사용할 수 있습니다.

상세 설계와 저장 방향은 [아키텍처 문서](docs/ARCHITECTURE.ko.md)를 참고하세요.

Adapter는 입력·설정 schema와 resource discovery/challenge workflow를 선언합니다. 새 adapter binary를 발견하려면 Core를 재시작해야 합니다. 기본 file secret은 권한이 제한되지만 저장 시 암호화되지 않습니다.

## 현재 상태

**Milestone 1과 external adapter protocol milestone 완료.**

현재 지원:

- Core와 분리된 실행 파일 adapter 구조. Core는 platform semantics를 알지 않으며 Owncast가 첫 adapter입니다.
- Adapter schema로 입력 및 설정 form을 생성하고, resource discovery와 설정 challenge를 generic workflow로 이어가는 기반.
- plugin 및 parent resource 설정을 합성하고, 현재 scope의 저장값과 effective 값을 구분해 노출.
- timeout/crash 후 다음 요청에서 backoff와 describe 재검증을 거쳐 adapter process를 lazy restart.
- 일반적인 HLS master/media playlist
- MPEG-TS/fMP4 형태 source object 직접 수집
- 저장 payload의 SHA-256 및 size 기록
- manifest snapshot
- 중복 억제, 제한된 retry, gap detection
- 프로세스 재시작 후 recording reload
- finite HLS VOD 재구성
- 브라우저 playback 및 seek

첫 실제 acceptance test는 공개 Owncast TV 예제 방송으로 수행했습니다. media segment 90개, 약 270초 VOD, detected gap 0개를 기록했고, 재시작/reload 및 0:00, 2:15, 4:27 seek에 성공했습니다. 저장 segment의 hash도 source 재요청본과 일치했습니다.

아직 미지원:

- 방송 metadata + chat timeline
- finalized TAR/index archive format
- HDD/NAS/LTO storage lifecycle
- 추가 platform adapter
- external audio rendition synchronization
- LL-HLS/delta playlist
- DRM workflow
- authentication/authorization
- export/transcoding

## 실행

Go 1.23 이상이 필요합니다.

```sh
mkdir -p adapters
go build -o adapters/integrated-recorder-adapter-owncast ./cmd/adapters/owncast
DATA_DIR=./data ADAPTER_DIR=./adapters ADDR=:8080 go run ./cmd/archiver
```

실행 후 `http://localhost:8080/`을 엽니다.

Docker 설정도 포함되어 있습니다.

```sh
docker compose up --build
```

컨테이너에서는 `/data`를 persistent recording storage로 사용합니다. 실제 Docker daemon에서 image build/runtime 검증은 아직 완료하지 않았습니다.

## API

| Method | Endpoint | 용도 |
| --- | --- | --- |
| `GET` | `/healthz` | Health check |
| `GET` | `/api/adapters` | 사용 가능한 adapter 목록 및 상태 |
| `GET` | `/api/adapters/{id}/schema` | adapter 입력/설정 schema |
| `GET` / `PUT` | `/api/adapters/{id}/config` | plugin 정의 설정. secret 값은 다시 반환하지 않음 |
| `POST` | `/api/recordings` | 녹화 시작 |
| `GET` | `/api/resolve-workflows/{id}` | 일시 중단된 resource/configuration workflow 조회 |
| `POST` | `/api/resolve-workflows/{id}/continue` | 답변 제출 및 workflow 재개 |
| `GET` | `/api/recordings` | 녹화 목록 |
| `GET` | `/api/recordings/{id}` | 녹화 상세 |
| `POST` | `/api/recordings/{id}/stop` | 녹화 중지 |
| `GET` | `/api/recordings/{id}/play/master.m3u8` | 생성된 VOD master playlist |
| `GET` | `/api/recordings/{id}/play/tracks/{track}/playlist.m3u8` | 생성된 VOD media playlist |
| `GET` | `/api/recordings/{id}/play/segments/{segmentID}` | 저장된 원본 payload |

첫 adapter로 녹화 시작 예시:

```sh
curl -X POST http://localhost:8080/api/recordings \
  -H 'Content-Type: application/json' \
  -d '{"adapter_id":"owncast","input":{"source_url":"https://watch.owncast.online"},"title":"optional title"}'
```

## 개발

```sh
go test -race -count=1 ./...
go vet ./...
go build ./...
```

## Roadmap

- [x] 원본 segment acquisition + restart-safe VOD playback
- [x] platform-agnostic Core + external Adapter Protocol v1 + Owncast binary
- [x] resource discovery, configuration inheritance, and challenge/resume foundation
- [ ] 방송 metadata + chat timeline
- [ ] Finalized archive packaging + random-access index
- [ ] 전체 browser UI
- [ ] CHZZK, SOOP, Twitch 등 추가 platform adapter
- [ ] HDD/NAS/LTO를 포함한 hot/cold storage lifecycle
- [ ] Optional export pipeline

## 문서

- [아키텍처](docs/ARCHITECTURE.ko.md)

## 라이선스

GNU Affero General Public License v3.0 only (**AGPL-3.0-only**). 자세한 내용은 [LICENSE](LICENSE)를 참고하세요.
