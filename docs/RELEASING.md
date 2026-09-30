# Building signed application releases

Official application releases are built for `linux/amd64` and `linux/arm64` from
`v*` tags by `.github/workflows/release.yml`. The workflow runs the web checks
and Go test suite before building the Runtime Host, Control Plane, Recorder
Engine, and first-party Owncast adapter runtime.

Each target has its own signed manifest and artifact set:

```text
release-linux-amd64.json
release-linux-amd64.json.sig
runtime-host-linux-amd64
control-plane-linux-amd64
recorder-engine-linux-amd64
adapter-runtime-linux-amd64
```

The same names use `arm64` for the other target. The `adapter-runtime` role is
the Owncast adapter executable; generic adapter host code is part of the
Control Plane and Recorder Engine binaries. `cmd/release-pack` uses fixed v1
protocol/schema compatibility declarations and hashes the exact packaged
regular files. Compatibility is not inferred from semantic version ordering.
The published release also includes `SHA256SUMS` for all platform manifests,
signatures, and executable artifacts.

The release workflow requires the repository secret
`IR_RELEASE_SIGNING_PRIVATE_KEY_BASE64`, containing a base64-encoded Ed25519
private key, and the non-secret repository variable
`IR_RELEASE_SIGNING_KEY_ID`. It fails closed if either value is missing or the
private key is malformed. Production private signing material must remain in
the secret store; no development or test key is used to publish releases.

The Runtime Host must separately be configured with the matching trusted
Ed25519 public key under the same key ID before it can install a remote
release. The release manifest and signature establish authenticity only when
verified against that externally provisioned trust root. In a deployed Host,
`GET /api/runtime/update` reports status and authenticated, CSRF-protected
`POST` requests to `/check`, `/stage`, `/activate`, and `/rollback` operate the
application release lifecycle. Development builds and deployments without a
trust root fail closed. This updates the application Control/Engine generation;
it does not replace the Runtime Host or container image.

Build identities are injected into Runtime Host, Control Plane, and Recorder
Engine through `internal/buildinfo` linker variables. Local development builds
retain their explicit `dev` identity and cannot install remote updates. The
current release contains the Owncast `adapter-runtime` artifact, but adapter
plugins are still discovered from the configured `/adapters` and
`/external-adapters` directories; a separately supervised, independently
updatable Adapter Runtime/Plugin Store is not implemented yet.
