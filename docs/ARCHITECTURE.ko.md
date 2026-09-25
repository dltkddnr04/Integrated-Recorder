# Integrated Recorder 아키텍처

[README](../README.md) | [English README](../README.en.md)

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
 Browser ─── HTTP API / VOD ─── Core
                                  │
                    ┌─────────────┴─────────────┐
                    │                           │
              adapterhost                 HLS acquisition
                    │                           │
          framed JSON over stdio                │
                    │                           ▼
          external adapter binary ── resolve  source manifests
                                                │
                              Original segments + snapshots
                                                │
                                          Working storage
```

현재 package 경계:

- `internal/adapterproto` — newline-delimited JSON Protocol v1 envelope, schema, resource, media source, interaction types
- `internal/adapterhost` — 설정된 adapter directory 검색, 독립 프로세스 실행/handshake/request/shutdown 관리
- `internal/pluginconfig` — opaque 설정과 별도 secret store. 파일 구현은 경로를 hash하고 제한된 권한으로 저장
- `internal/interaction` — generic interaction 진행 상태의 최소 state machine
- `internal/adapters/owncast` + `cmd/adapters/owncast` — standalone Owncast adapter만 포함. Core에서 이 package를 import하지 않음
- `internal/hls` — 공통 HLS parsing 및 rendition selection
- `internal/acquire` — recording lifecycle, polling, retry, deduplication, acquisition, gap detection
- `internal/storage` — recording directory, sidecar, manifest snapshot, payload access
- `internal/network` — bounded HTTP client와 source-address validation
- `internal/server` — control API, 최소 browser page, VOD playlist generation, segment serving
- `cmd/archiver` — adapter discovery, service wiring과 HTTP lifecycle

## External adapter Protocol v1

각 adapter는 Core에 정적으로 연결된 Go package가 아니라 독립 실행 파일입니다. Core는 `ADAPTER_DIR`만 검색하며 `integrated-recorder-adapter-*` 이름의 executable regular file만 시작합니다. `PATH` 전체는 검색하지 않습니다. adapter 추가/교체는 Core를 다시 빌드하지 않고 이 directory에 binary를 배치하는 것으로 가능합니다.

IPC는 stdin/stdout newline-delimited JSON이며 한 줄이 한 message입니다. 각 envelope는 protocol version, request ID, method를 포함하고 response는 같은 ID와 result 또는 `{code, message, details}` error를 돌려줍니다. frame 크기는 제한됩니다. 한 adapter process 내 request는 직렬화되고 timeout, malformed frame, version/ID 불일치 또는 process exit는 해당 adapter만 unavailable로 처리합니다. stdout은 protocol 전용이고 진단은 별도 stderr로 보냅니다. Core는 `describe` handshake에서 protocol version과 descriptor를 검증한 후 장기 실행 process를 유지하며 shutdown 때 graceful request 후 종료합니다.

v1의 사용 operation은 `describe`, `resolve`, `shutdown`입니다. `get_status`, `configure`, `interaction.begin`, `interaction.continue`, `metadata`, `events`, `refresh`는 generic operation 이름으로 예약되어 있으며 아직 실제 동작을 제공하지 않습니다. 알 수 없는 operation은 구조화된 `unsupported_method` error를 반환합니다.

Descriptor는 adapter ID/name/version, protocol version, opaque capability string, 입력 및 설정 schema, adapter가 선언한 opaque resource type, supported media type을 포함합니다. Core는 capability 값이나 resource type 이름의 플랫폼 의미를 해석하지 않습니다. Core의 media dispatch는 현재 공통 HLS acquisition을 위한 `hls` type만 인식합니다.

## Schema, resource, 설정 및 secret

Adapter 정의 schema는 UI가 adapter 전용 코드를 추가하지 않고 form을 그릴 수 있도록 field key, control, label, description, required/default, constraints, options, opaque `visible_when` 값을 표현합니다. control primitive는 `text`, `secret`, `number`, `boolean`, `select`, `multi-select`, `textarea`, `action`, `status`입니다. 선택형 control에는 option 목록이 필요합니다. schema validation은 key/control/constraint 형태만 확인하고 field key의 의미는 알지 못합니다.

Resource는 `{resource_type, resource_id, parent}`처럼 opaque 값과 재귀적 parent ref로 표현됩니다. adapter가 선언한 resource type과 parent-type 관계는 descriptor에 담기며 Core가 값을 비교하는 목적은 해당 adapter schema 범위를 찾고 해당 scope 문서를 구분하는 것뿐입니다. display name과 opaque attributes를 담는 일반 Resource 타입도 준비되어 있습니다. `account`, `channel`, `recording` 같은 Core enum이나 resource ID 기반 경로는 사용하지 않습니다.

설정 scope는 plugin ID와 선택적인 전체 resource ref입니다. 일반 JSON config와 secret은 별도 interface/store로 유지합니다. 현재 local file backend는 SHA-256 scope key로 경로를 만들고 directory는 `0700`, 파일은 `0600`으로 저장합니다. **이 파일 backend는 암호화하지 않습니다.** 운영 시 data directory 접근을 제한해야 하며 향후 `SecretStore` 구현을 encrypted vault로 교체할 수 있습니다. API GET은 secret 원문을 반환하지 않고 key별 `configured` boolean만 돌려줍니다. Resolve 때만 해당 scope의 configuration/secrets를 adapter process stdin protocol로 전달하며 recording metadata에는 input/config/secret을 저장하지 않습니다.

## Generic interaction model

Protocol message는 `action`, `prompt`, `secret_prompt`, `navigate`, `display`, `status`, `complete`, `error` 타입과 opaque field/data를 표현할 수 있습니다. Core의 interaction tracker는 interaction ID 단위로 active progress message를 누적하고 `complete` 또는 `error`를 terminal state로 처리합니다. 실제 인증 provider, browser handoff, CAPTCHA/OTP workflow는 이 milestone에서 구현하지 않았습니다.

## Resolve와 공통 media acquisition

Core는 recording 시작 시 `{adapter_id, input, resource?, title?}`를 받습니다. adapter-specific input은 JSON object로만 다루고 저장하지 않습니다. Adapter의 `resolve`는 media type, manifest URL, HTTP headers, optional session reference, opaque refresh/metadata를 반환합니다. Core는 manifest URL을 기존 SSRF validator로 검사하고 이후 dial도 safe HTTP client가 재검증합니다. Adapter가 제공한 header는 resolve된 manifest origin에만 전달하고 다른 origin URL/redirect로는 전달하지 않습니다.

HLS master/media parsing, rendition selection, sequence 추적, gap detection, init/media segment 원본 byte 다운로드, SHA-256, recording persistence는 계속 Core의 `internal/hls`와 `internal/acquire` 책임입니다. Adapter는 플랫폼별 URL을 공통 media source로 resolve할 뿐 manifest parsing이나 segment 처리에 참여하지 않습니다.

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
