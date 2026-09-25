# Integrated Recorder

[English](README.md) | **한국어**

**Integrated Recorder**는 Go로 작성된 헤드리스(segment-native) 라이브 스트림 아카이빙 서버입니다.

이 프로젝트의 핵심 아이디어는 단순합니다.

> 원본 스트림을 먼저 보존한다. 브라우저 재생, 검색 인덱스, 내보내기, 썸네일, 향후 UI 등 나머지는 모두 보존된 원본 데이터에서 다시 만들 수 있는 projection이어야 한다.

Integrated Recorder는 녹화 경로에서 **FFmpeg나 Streamlink를 사용하지 않습니다.** 대신 소스 manifest를 추적하고, 원본 media object를 직접 다운로드하며, byte를 변경하지 않고 그대로 보존하고, timeline 재구성에 필요한 metadata를 함께 기록한 뒤 나중에 브라우저에 VOD 형태로 제공합니다.

장기적으로는 Docker에서 헤드리스로 상시 동작하고, 모든 기능을 브라우저에서 제어하며, 영상과 함께 방송 metadata와 채팅도 보존하고, finalized archive를 canonical recording 자체를 변경하지 않은 채 SSD/HDD/NAS/LTO 사이에서 이동할 수 있는 self-hosted archival system을 목표로 합니다.

> [!IMPORTANT]
> 이 프로젝트는 아직 초기 개발 단계입니다. Milestone 1 — 원본 segment 수집, 재시작 안전한 보존, 브라우저 VOD 재구성 — 은 실제 end-to-end 검증까지 완료했습니다. 아래에 설명된 장기 기능 중 상당수는 현재 기능이 아니라 아키텍처 방향입니다.

## 왜 segment-native인가?

일반적인 "녹화기"는 최종 출력 파일을 중심으로 생각하는 경우가 많습니다.

```text
라이브 스트림
    ↓
녹화기 / 트랜스코더
    ↓
단일 미디어 파일
```

Integrated Recorder는 **스트림이 제공한 원본 object 자체**를 우선합니다.

```text
라이브 스트림
    ↓
manifest 추적
    ↓
원본 media segment + timing metadata
    ↓
canonical recording
    ├── 브라우저 VOD
    ├── 향후 채팅 replay
    ├── 향후 archive packaging
    └── 선택적 export/transcode
```

이 방식은 acquisition path를 단순하고 손실 없이 유지합니다.

- 녹화 중 decode/encode를 하지 않음
- 녹화 중 transcoding을 하지 않음
- 녹화 중 remux가 필수가 아님
- acquisition에 FFmpeg/Streamlink가 필요하지 않음
- 원본 payload byte를 그대로 보존
- segment hash로 무결성 검증 가능
- 먼저 하나의 거대한 미디어 파일을 만들지 않아도 playback 재구성 가능

향후 사용자가 일반적인 MP4/MKV 파일이나 실제 transcode를 명시적으로 요청하는 경우에는 FFmpeg를 **선택적 export backend**로 지원할 수 있습니다. 하지만 FFmpeg는 recorder core의 일부가 아닙니다.

## 현재 상태

### Milestone 1 — 완료

현재 구현은 다음 기능을 제공합니다.

- headless Go HTTP server 실행
- Owncast instance를 공식 HLS entry point로 resolve
- HLS master/media playlist parsing
- 외부 audio group이 필요하지 않은 muxed rendition 중 최고 bandwidth 선택
- sliding live playlist polling
- 새로운 media sequence 감지
- 중복 acquisition 억제
- 제한된 범위의 네트워크 재시도
- 건너뛴 sequence range와 명시적 `EXT-X-GAP` 감지
- init/media payload를 디스크에 원본 byte 그대로 저장
- payload size와 SHA-256 기록
- manifest snapshot과 hash 저장
- self-describing `recording.json` 저장
- 프로세스 재시작 후 기존 recording reload
- 비정상적으로 남은 in-progress 상태를 데이터 손실 없이 `interrupted`로 전환
- 저장된 segment에서 finite HLS VOD projection 생성
- 저장된 원본 segment byte를 HTTP로 직접 제공
- 브라우저에서 재구성된 VOD 재생 및 seek

Milestone 1에는 의도적으로 **application database가 없습니다.** recording directory 자체에 녹화를 다시 읽고 재생하는 데 필요한 정보가 들어 있습니다.

### 현재 플랫폼 지원

현재 platform adapter는 **Owncast** 하나만 구현되어 있습니다.

Owncast는 HLS entry point가 공개 문서로 규정되어 있고 동작이 예측 가능하므로, 플랫폼별 reverse engineering과 recorder core 검증을 분리하기에 적합합니다. Owncast는 현재 검증용 target이지, 프로젝트가 Owncast만 지원한다는 의미가 아닙니다.

현재 adapter는 Owncast instance base URL을 다음 경로로 resolve합니다.

```text
/hls/stream.m3u8
```

playlist parsing, rendition selection, segment acquisition, integrity metadata, VOD reconstruction은 Owncast adapter가 아니라 공통 HLS layer가 담당합니다.

### 현재 HLS 범위

현재 지원:

- 일반적인 HLS master/media playlist
- media sequence
- target duration
- `EXTINF`
- `PROGRAM-DATE-TIME`
- `EXT-X-MAP`
- byte range
- discontinuity
- `EXT-X-GAP`
- `ENDLIST`
- MPEG-TS 및 fMP4 형태 source payload 저장

아직 미지원:

- DRM/encrypted playback workflow
- external audio rendition synchronization
- LL-HLS parts
- delta playlist
- alternate rendition synchronization
- 현재 선택된 단일 muxed rendition을 넘어서는 multi-track archive

## End-to-end 검증

Milestone 1은 공개 Owncast TV 예제 스트림으로 실제 검증했습니다.

실제 라이브 방송을 약 4분간 녹화한 acceptance run 결과:

- **media segment 90개** 수집
- 재구성된 VOD timeline 약 **270초**
- 감지된 gap **0개**
- source에서 다시 받은 segment와 저장본의 size 및 SHA-256 일치
- 같은 data directory를 사용해 프로세스 종료 후 재시작
- 재시작 뒤 기존 recording 재발견
- 브라우저 playback 성공
- **0:00**, **2:15**, **4:27** 위치 seek 성공
- playback endpoint가 저장된 원본 segment byte를 그대로 반환
- FFmpeg 또는 Streamlink process/dependency를 사용하지 않음

실제 테스트 과정에서 Owncast가 `PROGRAM-DATE-TIME` timezone offset을 `+0000` 형태로 제공하는 호환성 이슈도 발견했습니다. parser가 이 형식을 허용하도록 수정했고 regression test를 추가했습니다.

현재 자동 검증은 다음 명령으로 통과합니다.

```sh
go test -race -count=1 ./...
go vet ./...
go build ./...
```

Docker 설정은 포함되어 있지만, 초기 acceptance run 당시 Docker를 사용할 수 없어 실제 image build/runtime 검증은 아직 완료하지 못했습니다.

## 아키텍처

현재 코드는 의도적으로 작지만, package 경계는 장기 시스템 구조에 맞춰져 있습니다.

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

현재 package 책임:

- `internal/platform/owncast` — Owncast 전용 stream discovery만 담당
- `internal/hls` — HLS parsing 및 rendition selection
- `internal/acquire` — recording lifecycle, live playlist polling, segment acquisition, retry, deduplication, gap detection
- `internal/storage` — self-describing recording directory, sidecar, manifest, payload access
- `internal/network` — bounded HTTP client와 public-address validation
- `internal/server` — control API, 최소 browser page, generated VOD playlist, segment serving
- `cmd/archiver` — process wiring과 HTTP server lifecycle

중요한 아키텍처 원칙은 **platform-specific discovery를 recorder core 바깥에 유지하는 것**입니다. CHZZK, SOOP, Twitch 또는 다른 서비스를 추가할 때 HLS acquisition engine이 해당 서비스의 URL 규칙이나 API semantics를 알아야 해서는 안 됩니다.

## Canonical data와 projection

장기 모델은 다음과 같습니다.

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

projection은 버려도 다시 만들 수 있어야 합니다. index가 사라지거나 애플리케이션을 새로 작성하더라도 정상적인 archive를 해석할 수 있어야 합니다.

그래서 장기 archive format 역시 특정 private database schema가 없어도 이해하고 복구할 수 있는 self-describing 형태를 지향합니다.

## Recording layout

현재 recording은 대략 다음과 같은 구조입니다.

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

media payload는 decode나 변환 없이 저장됩니다. metadata는 별도로 저장하여 source object 자체를 독립적으로 검증할 수 있게 합니다.

각 captured media object에는 다음과 같은 정보를 보존할 수 있습니다.

- track ID
- sequence number
- source URI
- duration
- 가능한 경우 program date/time
- init-segment reference
- byte range
- discontinuity state
- storage path
- payload size
- SHA-256

Manifest snapshot 역시 size, SHA-256, fetch time, source URI를 함께 저장합니다.

## 브라우저 재생

완료되었거나 interrupted 상태인 recording은 새로운 미디어 파일로 합쳐지는 대신 generated HLS VOD로 노출됩니다.

개념적으로는 다음과 같습니다.

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

따라서 storage representation은 segment-native 상태를 유지하면서도 브라우저에서는 archive timeline을 seek할 수 있습니다.

Safari는 native HLS를 사용할 수 있습니다. 현재 최소 UI는 native HLS를 지원하지 않는 브라우저에서 jsDelivr의 hls.js 1.5.17을 fallback으로 사용합니다.

codec 호환성은 source media와 browser 조합에 따라 달라집니다. Integrated Recorder는 단지 브라우저 호환성을 맞추기 위해 녹화물을 임의로 transcode하지 않습니다.

## HTTP API

현재 endpoint:

| Method | Endpoint | 용도 |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness check |
| `POST` | `/api/recordings` | 녹화 시작 |
| `GET` | `/api/recordings` | 녹화 목록 |
| `GET` | `/api/recordings/{id}` | 녹화 상세 |
| `POST` | `/api/recordings/{id}/stop` | 활성 녹화 중지 |
| `GET` | `/api/recordings/{id}/play/master.m3u8` | Generated VOD master playlist |
| `GET` | `/api/recordings/{id}/play/tracks/{track}/playlist.m3u8` | Generated VOD media playlist |
| `GET` | `/api/recordings/{id}/play/segments/{segmentID}` | 저장된 원본 payload |

Owncast recording 시작 예시:

```sh
curl -X POST http://localhost:8080/api/recordings \
  -H 'Content-Type: application/json' \
  -d '{"source_url":"https://watch.owncast.online","title":"optional title"}'
```

현재 `source_url`에는 direct manifest URL이 아니라 Owncast **instance base URL**을 넣어야 합니다.

## 실행

### 로컬

Go 1.23 이상이 필요합니다.

```sh
DATA_DIR=./data ADDR=:8080 go run ./cmd/archiver
```

실행 후:

```text
http://localhost:8080/
```

현재 페이지는 의도적으로 최소 구현입니다. 최종 제품 UI가 아니라 headless API와 playback path를 검증하기 위한 용도입니다.

### Docker

```sh
docker compose up --build
```

제공되는 Compose 설정은:

- `8080` 포트 노출
- `DATA_DIR=/data`
- `./data:/data` mount
- 중지되지 않는 한 service restart

최종 운영 모델은 persistent archive storage가 mount된 long-running headless container입니다.

## 네트워크 보안

현재 API는 사용자가 입력한 source URL을 받아 사용하므로 기본 network client는 localhost, private, loopback, link-local, multicast, unspecified address를 거부하고 실제 dial target을 다시 검증합니다.

이는 기본 보안 정책이지, 향후 local/self-hosted source를 절대 지원하지 않겠다는 의미는 아닙니다. 기본 보안을 약화시키지 않으면서 명시적으로 신뢰된 private-network source를 허용하는 설정을 추후 추가할 수 있습니다.

현재 control API에는 authentication/authorization이 없습니다. 신뢰할 수 없는 네트워크에 직접 노출하지 마십시오.

## 장기 archive 방향

working-directory representation과 최종 cold-storage representation은 의도적으로 분리합니다.

예상 방향:

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

최종 archive packaging에는 TAR이 유력한 후보입니다.

- 단순하고 장기간 복구 가능성이 높음
- LTO 같은 sequential media와 자연스럽게 맞음
- 수많은 작은 segment 파일이 cold storage에 흩어지는 문제를 줄임
- media 자체가 이미 압축되어 있어 whole-archive compression의 이득이 제한적
- 외부/sidecar index로 seek 가능한 storage에서 byte-offset random access 제공 가능
- index를 canonical data로 만들 필요가 없음

의도한 원칙은 다음과 같습니다.

> archive가 canonical이고, index는 재생성 가능한 acceleration structure다.

이 archive format은 **아직 구현되지 않았습니다.**

## Roadmap

가까운 개발 방향은 기능을 작게 나누어 순차적으로 진행합니다.

1. **원본 segment acquisition + restart-safe VOD playback** — 완료
2. **방송 metadata와 chat timeline** — title/category/state 변경과 chat/event를 동일 recording time axis에 보존
3. **Finalized archive packaging + random-access index** — HDD/NAS/LTO workflow를 고려한 구조
4. **Full browser control UI** — local CLI 없이 모든 recorder 관리와 VOD playback
5. **추가 platform adapter** — 예: CHZZK, SOOP, Twitch. 플랫폼별 특이사항은 recorder core 바깥에 유지
6. **Storage lifecycle/tiering** — finalized recording을 hot/cold storage 사이에서 이동
7. **Optional export pipeline** — 사용자가 명시적으로 요청할 때만 remux/transcode

실제 플랫폼 동작에서 새로운 요구사항이 발견되면 순서는 달라질 수 있습니다.

## Non-goals

적어도 recorder core에서는 다음을 목표로 하지 않습니다.

- 녹화 중 모든 stream을 transcoding
- acquisition을 위해 FFmpeg/Streamlink를 필수 dependency로 사용
- 저장된 recording을 이해하기 위해 중앙 database를 필수로 요구
- 모든 recording을 즉시 하나의 monolithic media file로 변환
- acquisition engine 전체에 platform-specific behavior를 섞어 넣기

## 라이선스

아직 라이선스를 선택하지 않았습니다.

repository는 public이지만, LICENSE가 추가되기 전까지는 open-source license가 부여된 상태가 아닙니다. repository 공개 여부에서 라이선스를 추정하지 않고 명시적으로 결정할 예정입니다.
