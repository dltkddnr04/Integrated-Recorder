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

Adapter는 입력·설정 schema와 resource discovery/challenge workflow를 선언합니다. 새 adapter binary를 발견하려면 Core를 재시작해야 하며 설치된 adapter는 신뢰된 로컬 코드로 실행됩니다. 기본 file secret store는 권한이 제한되지만 저장 시 암호화되지 않습니다.

## 현재 상태

**Milestone 1과 external adapter protocol milestone 완료.**

React 관리 UI는 녹화 검색·페이지네이션, 태그·삭제, 무결성 확인·취소, 어댑터 관리, resource 탐색 capability, workflow, 알림, 설정·선택적 녹화 보존, 전역 검색, 로그 조회, thumbnail projection을 backend API에 연결합니다. 첫 관리자 설정은 `<DATA_DIR>/security/bootstrap-token`을 이용합니다. FFmpeg가 설치된 경우에만 별도의 remux export와 thumbnail 생성이 제공되며 canonical 녹화 데이터는 변경하지 않습니다.

현재 지원:

- Core와 분리된 실행 파일 adapter 구조. Core는 platform semantics를 알지 않으며 Owncast가 첫 adapter입니다.
- Adapter schema로 입력 및 설정 form을 생성하고, resource discovery와 설정 challenge를 generic workflow로 이어가는 기반.
- plugin 및 parent resource 설정을 합성하고, 현재 scope의 저장값과 effective 값을 구분해 노출.
- timeout/crash 후 다음 요청에서 backoff와 describe 재검증을 거쳐 adapter process를 lazy restart.
- adapter가 선언한 만료 media source refresh
- 완료 segment가 포함된 LL-HLS playlist를 포함하는 제한된 single-rendition HLS subset
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
- 암호화 HLS 및 partial-only/delta LL-HLS
- DRM workflow
- 다중 사용자·역할 기반 authorization
- transcoding 및 추가 export format

## 실행

Go 1.23 이상이 필요합니다.

```sh
mkdir -p adapters
go build -o adapters/integrated-recorder-adapter-owncast ./cmd/adapters/owncast
DATA_DIR=./data ADAPTER_DIR=./adapters ADDR=127.0.0.1:8080 go run ./cmd/archiver
```

실행 후 `http://localhost:8080/`을 엽니다.

Docker 설정도 포함되어 있습니다.

```sh
docker compose up --build
```

컨테이너는 named `/data` volume을 사용하며 control API port는 host loopback에 공개합니다. Image에는 `/adapters`의 Owncast binary가 포함됩니다. 추가 executable adapter를 `./adapter-binaries`에 넣으면 `/external-adapters`에 read-only mount되며 Core를 재시작한 뒤 발견됩니다. 인증 없는 control API는 신뢰하는 host/private network 또는 인증 reverse proxy 안에서만 사용하고 untrusted network에 직접 공개하지 마세요.

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
| `DELETE` | `/api/resolve-workflows/{id}` | 대기 workflow 취소 |
| `GET` | `/api/recordings` | 녹화 목록 |
| `GET` | `/api/v2/recordings` | 검색·필터·정렬·커서 페이지네이션 녹화 목록 |
| `GET` | `/api/dashboard` | 실제 녹화, 저장소, integrity, adapter 현황 |
| `GET` / `PUT` | `/api/recordings/{id}/tags` | 태그 관리 |
| `DELETE` | `/api/recordings/{id}` | 비활성 녹화 삭제 |
| `POST` | `/api/recordings/{id}/integrity/verify` | 비동기 원본 무결성 확인 |
| `POST` | `/api/integrity/jobs/{job_id}/cancel` | 실행 중인 무결성 확인 취소 |
| `GET` | `/api/logs` | bounded application request-log 조회 |
| `GET` | `/api/recordings/{id}/archive/index` | canonical archive object 목록 |
| `GET` | `/api/adapters/{id}/resources` | adapter가 resource browse capability를 선언한 경우 목록 조회 |
| `POST` | `/api/adapters/{id}/restart`, `/enable`, `/disable` | 발견된 adapter process 관리 |
| `POST` | `/api/recordings/{id}/exports` | FFmpeg 설치 시 MKV remux job 요청 |
| `GET` | `/api/recordings/{id}/thumbnail` | 생성된 thumbnail projection 읽기 |
| `POST` | `/api/recordings/{id}/thumbnail/regenerate` | FFmpeg 설치 시 thumbnail 다시 생성 |
| `GET` / `PUT` | `/api/settings` | 실제 지원되는 UI theme 및 integrity concurrency 설정 |
| `POST` | `/api/auth/login`, `/logout`, `/bootstrap` | single-admin session 인증 |
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

Web UI 개발에는 Node.js 20 이상이 필요합니다. Go API와 Vite 개발 서버를 별도 터미널에서 실행합니다.

```sh
go run ./cmd/archiver
npm --prefix web ci
npm --prefix web run dev
```

운영용 Go binary를 만들기 전에는 React UI asset을 embed 경로에 빌드합니다. `make build`는 UI build와 Go build를 순서대로 실행합니다.

```sh
npm --prefix web run build
go build ./...
# 또는
make build
```

최초 관리자 설정 token은 `<DATA_DIR>/security/bootstrap-token`에서 확인합니다.

검증 명령:

```sh
go test -race -count=1 ./...
go vet ./...
```

## Roadmap

- [x] 원본 segment acquisition + restart-safe VOD playback
- [x] platform-agnostic Core + external Adapter Protocol v1 + Owncast binary
- [x] resource discovery, configuration inheritance, and challenge/resume foundation
- [ ] 방송 metadata + chat timeline
- [ ] Finalized archive packaging + random-access index
- [x] management browser UI v2 및 실제 product API 기초
- [ ] CHZZK, SOOP, Twitch 등 추가 platform adapter
- [ ] HDD/NAS/LTO를 포함한 hot/cold storage lifecycle
- [x] Optional remux-only export pipeline (FFmpeg가 있을 때)

## 문서

- [아키텍처](docs/ARCHITECTURE.ko.md)

## 라이선스

GNU Affero General Public License v3.0 only (**AGPL-3.0-only**). 자세한 내용은 [LICENSE](LICENSE)를 참고하세요.
