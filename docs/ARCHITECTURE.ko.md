# Integrated Recorder 아키텍처

[README](../README.ko.md) | [English](ARCHITECTURE.md)

이 문서는 Integrated Recorder의 장기 아키텍처 방향을 기록합니다. 프로젝트 첫 화면에 넣기에는 너무 상세한 설계 내용을 README에서 분리해 두는 목적입니다.

## 설계 목표

Integrated Recorder는 단순한 "영상 파일 다운로더"가 아닙니다. 원본 스트림 자체, 즉 원본 media object와 나중에 방송을 재구성할 수 있는 metadata를 canonical data로 취급하는 라이브 스트림 아카이빙 시스템입니다.

핵심 원칙은 다음과 같습니다.

> 원본 데이터를 먼저 보존한다. 재생, index, export, UI view는 다시 만들 수 있는 projection으로 취급한다.

따라서 acquisition 과정에서는 media를 decode, encode, transcode, remux하지 않습니다. FFmpeg와 Streamlink는 recording path의 dependency가 아닙니다.

## Canonical data와 projection

장기적으로 canonical data는 다음을 포함합니다.

```text
Recording
├── original media objects
├── source/timing metadata
├── manifest history
├── broadcast metadata
└── chat/events
```

여기서 파생되는 projection은 다음과 같습니다.

```text
browser HLS VOD
database/search indexes
thumbnails
chat replay
MP4/MKV exports
other presentation formats
```

projection은 버려져도 다시 만들 수 있어야 합니다. application database나 index가 사라져도 정상적인 archive 자체는 이해 가능해야 합니다.

## 현재 아키텍처

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

현재 package 경계:

- `internal/platform/owncast` — 플랫폼별 stream discovery만 담당
- `internal/hls` — HLS parsing 및 rendition selection
- `internal/acquire` — recording lifecycle, polling, retry, deduplication, acquisition, gap detection
- `internal/storage` — recording directory, sidecar, manifest snapshot, payload access
- `internal/network` — bounded HTTP client와 source-address validation
- `internal/server` — control API, 최소 browser page, VOD playlist generation, segment serving
- `cmd/archiver` — process wiring과 HTTP lifecycle

플랫폼별 특수 동작은 recorder core 위쪽에 머물러야 합니다. CHZZK, SOOP, Twitch 등의 adapter를 추가할 때 HLS acquisition engine이 해당 플랫폼의 API semantics까지 알아야 해서는 안 됩니다.

## Recording lifecycle

현재 acquisition 중에는 database 의존적인 opaque format 대신 self-describing working directory를 사용합니다.

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

각 media object에는 다음 정보를 보존할 수 있습니다.

- track identity
- source sequence
- source URI
- duration
- 가능한 경우 program date/time
- init-segment reference
- byte range
- discontinuity state
- storage path
- payload size
- SHA-256

Manifest snapshot 역시 source URI, fetch time, size, SHA-256을 저장합니다.

서버가 재시작되면 이 파일들을 다시 읽습니다. 이전 프로세스의 `recording` 상태가 남아 있으면 `interrupted`로 바꾸되 이미 수집한 media는 그대로 유지합니다.

## Playback projection

중지/완료/interrupted 상태의 recording은 새로운 하나의 media file로 합치지 않습니다. 저장된 timeline을 기반으로 finite HLS VOD playlist를 생성합니다.

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

따라서 storage는 segment-native 상태를 유지하면서 브라우저에서는 seek 가능한 VOD로 재생할 수 있습니다.

source media codec이 브라우저에서 지원되는지는 별개의 문제입니다. 단지 브라우저 호환성을 맞추기 위해 recorder가 녹화물을 자동 transcode하지 않습니다.

## Metadata와 chat timeline

향후에는 영상/음성뿐 아니라 방송 중 발생하는 비미디어 event도 동일한 시간축에 보존해야 합니다.

예:

- 방송 제목 변경
- category 변경
- 방송 상태 변경
- 채팅 메시지
- moderation/system event
- 플랫폼 고유 raw event

공통 동작에 필요한 normalized field를 만들되, 당시에는 중요성을 몰랐던 정보를 미래에 다시 해석할 수 있도록 유용한 platform raw payload도 함께 보존하는 방향입니다.

## Finalized archive 방향

Working storage와 cold-storage representation은 의도적으로 분리합니다.

예상 lifecycle:

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

TAR은 final archive packaging의 유력한 후보입니다. 단순하고 장기 복구 가능성이 높으며, LTO 같은 sequential media와 자연스럽게 맞습니다. media segment는 이미 압축되어 있으므로 whole-archive compression은 핵심 목표가 아닙니다.

seek 가능한 storage에서는 sidecar index가 logical segment ID를 archive 내부 byte offset과 연결할 수 있습니다.

원칙은 다음과 같습니다.

> archive가 canonical이다. index는 acceleration structure이며 다시 만들 수 있어야 한다.

최종 archive format은 아직 구현되지 않았습니다.

## 네트워크 보안

현재 HTTP client는 localhost, private, loopback, link-local, multicast, unspecified source address를 거부하며 실제 dial target을 다시 검증합니다.

이는 안전한 기본값이지, 신뢰된 private-network source를 영구적으로 금지한다는 뜻은 아닙니다. 향후 기본 보안을 약화시키지 않는 명시적 설정으로 local/self-hosted source를 허용할 수 있습니다.

현재 control API에는 authentication/authorization이 없으므로 신뢰할 수 없는 네트워크에 직접 노출하면 안 됩니다.

## Milestone 1 검증

Milestone 1은 공개 Owncast TV 예제 스트림으로 실제 검증했습니다.

- media segment 90개
- 재구성된 VOD 약 270초
- detected gap 0
- source 재요청본과 저장 payload의 size/SHA-256 일치
- 프로세스 재시작 후 recording reload 성공
- 브라우저 playback 성공
- 0:00, 2:15, 4:27 seek 성공
- FFmpeg/Streamlink 미사용

실제 테스트 중 Owncast가 `PROGRAM-DATE-TIME` timezone offset을 `+0000` 형태로 제공하는 호환성 문제도 발견했고, parser 수정과 regression test를 추가했습니다.
