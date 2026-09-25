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

IPC는 stdin/stdout newline-delimited JSON이며 한 줄이 한 message입니다. 기존 v1 request/response envelope는 type 없는 wire format을 유지하고, response는 같은 request ID와 result 또는 `{code, message, details}` error를 돌려줍니다. 향후 비동기 확장을 위해 `{"protocol_version":1,"type":"notification","method":"...","params":{...}}` notification frame 형태를 예약했고 parser가 이를 request/response와 구분합니다. 현재 runtime은 notification을 전달하지 않으며 response를 기다리는 중 notification이 오면 protocol 오류로 안전하게 거부합니다. frame 크기는 제한됩니다. 한 adapter process 내 request는 직렬화됩니다. timeout, malformed frame, version/ID 불일치 또는 process exit가 발생하면 해당 process를 폐기합니다. 다음 요청 때 bounded backoff를 적용해 lazy restart하고 `describe`를 다시 수행하며 adapter ID/version과 호환 가능한 protocol identity가 일치할 때만 사용합니다. background 무한 재시작은 하지 않습니다. stdout은 protocol 전용이고 진단은 별도 stderr로 보냅니다. Core는 `describe` handshake 후 장기 실행 process를 유지하며 shutdown 때 graceful request 후 종료합니다.

v1의 사용 operation은 `describe`, `resolve`, `resolve.begin`, `resolve.continue`, `shutdown`입니다. `resolve_workflow` generic capability를 선언한 adapter는 resource가 미리 알려지지 않은 상태에서 opaque input을 받고 resource를 발견한 뒤 challenge 또는 media source를 반환할 수 있습니다. capability가 없는 adapter는 기존 `resolve`를 계속 사용합니다. `get_status`, `configure`, `interaction.begin`, `interaction.continue`, `metadata`, `events`, `refresh`는 아직 runtime 동작이 없습니다. 알 수 없는 operation은 구조화된 `unsupported_method` error를 반환합니다.

Descriptor는 adapter ID/name/version, protocol version, opaque capability string, 입력 및 설정 schema, adapter가 선언한 opaque resource type, supported media type을 포함합니다. Core는 capability 값이나 resource type 이름의 플랫폼 의미를 해석하지 않습니다. Core의 media dispatch는 현재 공통 HLS acquisition을 위한 `hls` type만 인식합니다.

## Schema, resource, 설정 및 secret

Adapter 정의 schema는 UI가 adapter 전용 코드를 추가하지 않고 form을 그릴 수 있도록 field key, control, label, description, required/default, constraints, options, inheritance, opaque `visible_when` 값을 표현합니다. control primitive는 `text`, `secret`, `number`, `boolean`, `select`, `multi-select`, `textarea`, `action`, `status`입니다. 선택형 control에는 option 목록이 필요합니다. Core는 input/configuration/challenge 값을 해당 schema로 검증하고 key의 플랫폼 의미는 알지 못합니다. action/status는 편집 가능한 값으로 제출하지 않습니다. recording input, plugin/resource 설정, workflow challenge는 같은 기본 renderer를 사용합니다.

Resource는 `{resource_type, resource_id, parent}`처럼 opaque 값과 재귀적 parent ref로 표현됩니다. adapter가 선언한 resource type과 parent-type 관계는 descriptor에 담기며 Core가 값을 비교하는 목적은 해당 adapter schema 범위를 찾고 해당 scope 문서를 구분하는 것뿐입니다. display name과 opaque attributes를 담는 일반 Resource 타입도 준비되어 있습니다. `account`, `channel`, `recording` 같은 Core enum이나 resource ID 기반 경로는 사용하지 않습니다.

설정 scope는 plugin ID와 선택적인 전체 resource ref의 chain입니다. plugin scope 뒤에 parent resource부터 현재 resource까지 적용합니다. Core는 parent 관계만 사용하고 type/id 의미는 해석하지 않으며 서로 무관한 resource chain을 섞지 않습니다. effective 설정은 일반 scope부터 구체적인 scope 순으로 합성합니다. 각 schema field의 `inherit` 값으로 상위 scope의 값/secret을 하위로 전달할지 결정합니다. control 종류를 예외로 취급하지 않으며 모든 field의 기본값은 `false`입니다. 현재 scope에 저장된 값은 해당 scope에서 항상 적용됩니다. API는 현재 scope의 stored 값과 합성된 effective 값을 구분하고, 상속 값에는 opaque source scope를 제공합니다. scope 저장은 부분 업데이트이며 required 값은 상위 scope 또는 workflow challenge에서 제공될 수 있습니다.

일반 JSON config와 secret은 backend-neutral한 별도 `ConfigStore`/`SecretStore` interface로 유지합니다. 현재 local file backend는 SHA-256 scope key로 경로를 만들고 directory는 `0700`, 파일은 `0600`으로 저장합니다. Secret 파일은 일반 config와 분리되고 권한이 제한되지만 **저장 시 암호화되지 않아 plaintext-at-rest입니다.** Core는 secret 값을 log에 쓰지 않고 API GET 응답은 원문 대신 key별 `configured` 상태만 돌려줍니다. 빈 secret 제출은 기존 값을 유지하며 `clear_secrets`로 명시한 경우에만 삭제합니다. Resolve 때 effective configuration/secrets가 adapter process stdin protocol로 전달되며 recording metadata에는 저장되지 않습니다. 이 interface는 향후 encrypted 또는 OS-backed backend로 교체할 수 있지만 현재 구현은 제공하지 않습니다.

## Generic interaction model

Protocol message는 `action`, `prompt`, `secret_prompt`, `navigate`, `display`, `status`, `complete`, `error` 타입과 opaque field/data를 표현할 수 있습니다. Core의 interaction tracker는 interaction ID 단위로 active progress message를 누적하고 `complete` 또는 `error`를 terminal state로 처리합니다. Workflow는 resource discovery, configuration/interaction challenge, 최종 media source 상태를 반환할 수 있습니다. challenge에는 opaque workflow ID와 schema가 포함되며 사용자는 `POST /api/resolve-workflows/{id}/continue`로 답을 제출합니다. Challenge가 허용하면 답을 일회성(ephemeral)으로 보내거나 발견된 resource(또는 plugin) scope에 저장할 수 있습니다. 대기 중 workflow는 현재 process 메모리에만 존재하여 Core 재시작 시 사라집니다. 실제 인증 provider, browser handoff, CAPTCHA/OTP implementation은 adapter 책임이며 아직 이 milestone에서 구현하지 않았습니다.

## Resolve와 공통 media acquisition

Core는 recording 시작 시 `{adapter_id, input, resource?, title?}`를 받습니다. adapter-specific input은 JSON object로만 다루고 adapter `input_schema`에 따라 검증하며 저장하지 않습니다. Workflow adapter라면 입력을 먼저 전달한 뒤에 resource를 발견하고, Core가 opaque resource chain의 configuration을 로드해 adapter를 재개합니다. 최종 media source는 media type, manifest URL, HTTP headers, optional session reference, opaque refresh/metadata와 선택적인 `request_policy.header_forwarding`을 포함합니다. 기본은 `same_origin`이며, adapter가 `allowlist`와 origin URL 목록을 선언하면 그 origin에도 adapter 제공 header를 전달할 수 있습니다. 현재 하나의 origin 정책이 adapter 제공 전체 header에 적용됩니다. 비교는 scheme, 대소문자 무시 hostname, effective port를 사용하는 정확한 origin 비교입니다. 모든 새 media URL과 redirect마다 정책을 다시 평가하고, redirect 시 이전 adapter header를 모두 지운 뒤 새 URL이 허용될 때만 다시 붙입니다. 이 정책은 header 전달만 허가하며 private IP/SSRF 접근 권한을 주지 않습니다. Core의 기존 source validator와 safe HTTP client의 주소/dial 검증이 계속 적용됩니다.

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

새 recording은 adapter ID/version, protocol version, 가능하면 descriptor fingerprint를 provenance로 저장합니다. 이 선택 필드는 기존 recording에 없어도 정상 로드됩니다. Adapter는 source URI를 `sensitive` 또는 `public`으로 분류할 수 있으며 기본은 `sensitive`입니다. 이번 milestone은 runtime fetch URI와 raw manifest snapshot을 원본 그대로 canonical recording data로 보존하므로 URL 내 credential도 그대로 남습니다. Recording directory는 `0700`, metadata file은 `0600` 권한을 사용합니다. API list/detail projection은 source URL과 manifest URI를 노출하지 않고 classification/provenance는 제공합니다.

설치된 adapter binary는 신뢰된 로컬 코드입니다. Core와 같은 OS 사용자로 실행되며 sandbox되지 않습니다. Adapter directory는 Core 시작 시 검색하므로 새 binary를 발견하려면 Core를 재시작해야 합니다. hot-load는 구현하지 않았습니다.

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
