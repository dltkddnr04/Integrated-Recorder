# Integrated Recorder 아키텍처

[한국어 README](../README.md) | [English README](../README.en.md)

이 문서는 현재 코드의 동작을 설명합니다. protocol에 예약만 된 형태와 향후 계획은 실제 구현과 구분해 적습니다.

## Core 불변 원칙

canonical archive는 방송사에서 받은 원본 media와 timeline을 재구성할 metadata입니다. acquisition 중 media를 decode, encode, transcode, remux하지 않습니다. Core에는 플랫폼 domain model이 없습니다. resource type, field key, adapter state, interaction data는 opaque string 또는 JSON으로 취급하고 플랫폼별 탐색은 external adapter process에 둡니다.

## 실행 구조와 package 책임

```text
Browser ── HTTP API / 생성 VOD ── Core
                                    ├─ adapterhost ── framed JSON/stdin/stdout ── adapter binary
                                    ├─ 공통 HLS parser/acquisition
                                    ├─ network safety policy
                                    └─ self-describing recording directory
```

- `internal/adapterproto` — 언어 중립 Protocol v1 envelope, descriptor/schema validation, resource, workflow, media source, refresh policy, adapter-owned state 메시지
- `internal/adapterhost` — 명시된 adapter directory 탐색, 장기 실행 process 관리, descriptor/resource 검증, 설정 해석, workflow session 관리
- `internal/pluginconfig` — 사용자 설정/secret과 adapter-owned opaque state/state secret을 분리 저장. interface는 backend-neutral하며 현재 구현은 file backend
- `internal/interaction` — 제한과 만료가 적용되는 generic interaction progress message
- `cmd/adapters/owncast`, `internal/adapters/owncast` — 첫 번째 standalone adapter. Core는 Owncast package를 import하지 않음
- `internal/hls` — 지원하는 HLS subset parser. `internal/acquire` — polling, refresh, retry, segment acquisition, sequence epoch, recording lifecycle
- `internal/network` — public destination 검증 및 매 연결마다 검증한 주소에 고정하는 dial
- `internal/domain` — adapter wire type과 분리된 archive type. `internal/storage` — 내구성 있는 payload와 self-describing metadata 기록. `internal/server` — API와 정적 관리 페이지

## Adapter process와 protocol

Adapter는 독립 실행 파일입니다. Core는 `ADAPTER_DIR`에 명시된 디렉터리에서 `integrated-recorder-adapter-*` 이름의 executable regular file만 찾으며 `PATH` 전체를 검색하지 않습니다. 새 binary를 추가해도 Core rebuild는 필요 없지만 검색은 시작 시 수행하므로 Core restart가 필요합니다. 설치된 adapter는 신뢰된 로컬 코드입니다. Core와 같은 OS user로 실행되며 sandbox되지 않습니다.

IPC는 크기가 제한된 newline-delimited JSON입니다. v1 기존 request/response wire 형식은 유지하며 request ID, protocol version, method, result, 구조화 error를 검증합니다. Parser는 typed frame과 예약된 `notification` 형태도 구분하지만 runtime은 비동기 notification을 전달하지 않습니다. response 대기 중 notification이 오면 protocol error로 처리합니다.

각 process의 호출은 직렬화됩니다. timeout, request 전송 후 cancellation, malformed/oversized output, 예상하지 않은 EOF, request ID 불일치, protocol 불일치, process exit는 해당 process를 unusable 상태로 만듭니다. 다음 operation에서 bounded backoff를 거쳐 필요할 때 lazy restart합니다. 매 restart마다 `describe`를 다시 호출하며 최초 discovery의 adapter ID, version, protocol version, canonical descriptor fingerprint가 모두 같아야 수락합니다. 무한 background restart loop는 없습니다. 종료 때 generic shutdown request를 보내고 제한 시간 안에 자식 process를 종료합니다.

v1은 descriptor가 관련 capability를 선언한 경우 `describe`, legacy `resolve`, `resolve.begin`, `resolve.continue`, `refresh`, `shutdown`을 구현합니다. 나머지 operation 이름은 예약되어 있거나 구조화된 unsupported error를 반환합니다. 문법에 맞는 알 수 없는 optional capability는 보존하고 무시할 수 있지만 protocol version 불일치는 거부합니다.

## Resource, 설정, workflow

Adapter descriptor는 opaque resource type과 허용된 parent type 관계를 선언합니다. Core는 API hint, config scope, workflow discovery, persistence target에 대해 resource 참조와 모든 parent edge를 이 선언에 따라 검증합니다. type 이름의 의미는 해석하지 않습니다. chain 길이는 제한되고 cycle은 허용되지 않습니다.

설정에는 stored와 effective 두 view가 있습니다. Effective 값은 plugin scope, 가장 상위 parent resource부터 하위 resource, 현재 resource 순서로 합성하고 구체적인 scope가 상위 값을 override합니다. 각 field의 `inherit`가 상위 값을 하위로 전달할지 결정하며, 모든 control의 기본값은 `false`입니다. `clear_values`는 local override를 지워 상속/default 상태로 되돌립니다. `clear_secrets`는 secret을 명시적으로 삭제합니다. 빈 ordinary value는 값이며 빈 secret 제출은 기존 값을 유지합니다.

`resolve.begin`은 안정된 resource ID를 알기 전에 adapter 정의 input을 받습니다. Adapter가 resource를 반환하면 Core가 chain을 검증하고 해당 설정, secret, adapter state를 읽은 후 workflow를 이어갑니다. Challenge는 같은 schema vocabulary와 generic workflow state를 사용합니다. 각 challenge field는 `forbidden`, `optional`, `required` persistence mode와 `plugin`, `current_resource`, 명시적 `resource` target 중 하나를 선언할 수 있습니다. 명시적 resource는 검증된 현재 chain에 포함되어야 합니다. Persistent challenge field는 target scope의 descriptor schema에도 같은 key/control로 선언되어야 하며 그렇지 않은 값은 ephemeral입니다. 이로써 저장된 값이 다음 resolution에서 누락되는 일을 막습니다.

Workflow session은 process-local이며 이를 생성한 adapter process generation에 묶입니다. Process가 restart되면 stale workflow ID를 새 process로 전달하지 않고 정해진 만료 오류를 반환합니다. 유휴 TTL은 30분, 동시 session은 최대 128개, 전체 누적 transition은 최대 32회입니다. `DELETE /api/resolve-workflows/{id}`로 취소할 수 있습니다. 만료/취소 시 title과 연결된 interaction progress도 제거합니다. Core가 재시작되면 workflow session은 유지되지 않습니다.

Browser는 recording input, 설정, challenge에서 같은 schema renderer를 사용합니다. `visible_when` 문법은 `field` 비교 연산 `equals`, `not_equals`, boolean `truthy`와 재귀적인 `all`/`any`입니다. Default, select/multi-select, 상속 source, 명시적 clear 동작을 지원합니다. Secret 원문은 다시 읽지 않습니다. Prompt/display/status는 text로 렌더링하며 navigation은 HTTP(S) URL만 허용하고 `noopener noreferrer`로 엽니다. Adapter HTML/script는 실행하지 않습니다.

## Adapter-owned state와 refresh

Adapter-owned opaque state는 사용자 설정과 별도입니다. State value와 state secret은 adapter 및 선택적인 resource scope에 저장되며 Core는 key 의미를 해석하지 않습니다. Mutation target은 현재 검증된 chain에 속해야 합니다. 다음 resolve/refresh 때 다시 adapter로 전달하고 adapter process 재시작 후에도 보존됩니다. Recording metadata나 public API에는 들어가지 않습니다.

기본 file state-secret backend는 일반 state와 분리되고 파일 권한이 제한되지만 **at-rest encryption은 제공하지 않습니다.** 사용자 secret backend도 같은 한계가 있습니다. 둘 다 backend-neutral interface를 사용해 향후 encrypted 또는 OS-backed backend로 교체할 수 있지만 현재 제공하지 않습니다. 암호화 vault처럼 설명하지 않습니다.

Refresh capability를 지원하는 adapter는 만료 시각, 선행 refresh 시간, refresh를 유발할 HTTP status를 선언할 수 있습니다. Core는 status만으로 token 만료를 추측하지 않습니다. 선제 refresh와 adapter가 선언한 status refresh만 수행하고 새 media source는 adapter가 만듭니다. Core는 새 media와 request policy, public manifest URL을 검증한 뒤 staged adapter state 변경을 저장하고 활성 URL/header/forwarding policy/refresh policy를 교체합니다. 검증이나 refresh 실패는 활성 source를 대체하지 않습니다. 외부에 노출되는 refresh error는 generic하며 signed URL과 secret을 포함하지 않습니다.

## Media acquisition과 HLS 지원 범위

Adapter는 input을 generic media source로 resolve합니다. Playlist parsing, rendition selection, request/header policy, retry, segment 원본 byte 저장, SHA-256, gap detection, VOD 생성은 Core 책임입니다. Adapter header 전달 기본값은 same-origin입니다. Adapter가 정확한 origin allowlist를 선언할 수 있지만 Core는 요청 및 redirect마다 다시 검사하고 SSRF/public-address 정책도 별도로 강제합니다.

현재 HLS subset은 하나의 자체 완결 rendition, MPEG-TS 또는 fMP4 media object, init map, 명시적이고 overflow-safe한 byte range, discontinuity, program date/time, 완료된 `EXTINF` segment를 지원합니다. 동일 playlist에 완료 segment가 있으면 LL-HLS partial tag는 무시할 수 있습니다. 암호화 HLS, external audio/video/subtitle rendition, I-frame-only, URI 변수 대체(`EXT-X-DEFINE`), delta(`EXT-X-SKIP`), partial-only LL-HLS는 명시적으로 거부합니다. DASH, 별도 subtitle rendition, DRM/key acquisition은 구현하지 않았습니다. 거부 동작은 결정적이며 source media byte는 수정하지 않습니다.

Media sequence는 source identifier일 뿐 archive identity가 아닙니다. Core는 source epoch와 증가하는 archive ordinal을 함께 기록하여 sequence reset 이후 재사용된 번호가 새 segment 수집을 막지 않도록 합니다. VOD는 archive ordinal 순서로 만들고 epoch 경계 또는 감지한 gap에 discontinuity를 삽입합니다.

## Storage, privacy, recovery

각 recording은 `recording.json`, raw manifest snapshot, payload, segment sidecar를 포함하는 self-describing directory입니다. Database가 필수는 아닙니다. 새 directory는 초기 metadata를 hidden incomplete 경로에 먼저 기록한 뒤 원자적으로 공개합니다. Rename 전 file sync를 수행하고 지원되는 환경에서는 parent directory도 sync합니다.

Segment와 manifest payload 저장 후 각각 sidecar를 만들고 root recording metadata를 갱신합니다. 시작 시 유효 sidecar가 있으면 crash 직전에 root document에서 누락된 segment 또는 manifest snapshot을 복원할 수 있습니다. Sidecar 없는 payload는 orphan으로 보존하고 보고합니다. 손상되거나 충돌하는 sidecar는 조용히 attach하지 않고 보고합니다. Incomplete/corrupt recording은 보존하고 보고하지만 나머지 정상 recording은 계속 읽습니다. 비정상 종료 후 active recording은 `interrupted`가 되고 pending segment는 명시적인 gap으로 기록됩니다.

`internal/domain` archive type은 protocol wire type과 분리되어 있습니다. 기존 JSON field 이름을 유지하고 epoch, ordinal, provenance, URI classification은 optional이므로 구 recording도 읽을 수 있습니다. Adapter ID/version/protocol version/fingerprint는 provenance이며 credential, header, adapter state는 recording metadata에 복사하지 않습니다. Source URL과 raw manifest snapshot은 credential이 포함된 경우에도 원본 canonical data로 보존합니다. 따라서 recording directory 권한을 제한하고 public list/detail 응답에서는 URL을 제거합니다.

Recording directory는 `0700`, metadata/payload/sidecar 파일은 `0600`입니다. File backend는 at-rest encryption을 제공하지 않습니다. 인증/권한 기능이 없는 management API는 신뢰 경계 내부의 control plane입니다. Local 실행은 loopback에 bind하고 Compose도 host의 `127.0.0.1`에만 port를 공개합니다. Untrusted network에 직접 노출하지 말고 신뢰된 private network 또는 인증 reverse proxy를 사용하세요. 관리 페이지는 로컬 pinned hls.js 1.5.17(Apache-2.0)과 restrictive CSP/security header를 사용합니다.

## Server lifecycle과 Docker

SIGINT/SIGTERM에서 Core는 신규 HTTP 요청을 받지 않은 뒤 모든 recording worker를 먼저 취소하고 종료 및 durable terminal state 저장을 기다린 다음 adapter process를 종료합니다. Shutdown에는 제한 시간이 있습니다. Compose는 worker 종료 제한 시간보다 긴 45초 grace period를 사용합니다. 실제 crash에서는 active recording이 재시작 후 `interrupted`로 표시됩니다.

Container는 UID 10001로 실행합니다. Compose는 named `/data` volume을 쓰며 control port는 host loopback에 공개합니다. Image에는 Owncast adapter가 `/adapters`에 포함됩니다. 추가 executable adapter는 `./adapter-binaries`에 두고 `/external-adapters`에 read-only mount합니다. Compose 실행 전 executable bit를 설정해야 합니다. Core restart 후 새 binary를 발견하며 hot reload는 없습니다. API authentication이 없으므로 `ADDR`는 신뢰 경계 안에 두어야 합니다.

## Playback과 의도적으로 미구현인 항목

중지/완료/interrupted recording의 저장 segment를 참조하는 finite HLS VOD manifest를 생성합니다. Segment endpoint는 저장된 원본 byte를 직접 반환하며 파일을 이어 붙이거나 remux하지 않습니다. 재생 가능한 codec인지 여부는 browser 지원에 달려 있습니다.

미구현: chat/metadata timeline, notification runtime, 실제 platform authentication flow, 추가 platform adapter, Core 재시작을 넘는 workflow persistence, encrypted HLS, external rendition 동기화, DASH, TAR/archive finalization, LTO, export/transcoding, database, authentication/authorization, adapter sandbox, adapter hot reload.
