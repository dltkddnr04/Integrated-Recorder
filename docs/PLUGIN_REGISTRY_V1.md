# Plugin Registry v1

Integrated Recorder's Plugin Registry is an approval and distribution index. It is **not a build service**: publishers build adapter executables in their own CI and host the resulting artifacts on GitHub Releases, a CDN, or another HTTPS host. The registry document approves an exact release artifact by recording its platform, filename, byte size, and lowercase SHA-256 digest.

For v1, the configured registry repository is the approval authority. Artifact hosting is transport only. Publisher PKI and detached plugin signatures are not part of this version. The Runtime Host obtains the catalog from its operator-configured fixed HTTPS URL, then accepts an artifact only when its downloaded bytes match both the registry size and SHA-256. A change to hosted bytes therefore fails verification. A successful hash check is followed by the normal Protocol v1 executable probe and exact descriptor identity check; only then can the binary be published into the Host-owned desired source snapshot and passed to the existing immutable adapter catalog.

## Configuration and platform selection

Set `IR_PLUGIN_REGISTRY_URL` on the Runtime Host to the HTTPS URL of the static v1 JSON document. There is no built-in production URL. If the setting is empty, the registry is reported as unavailable/not configured and local adapters continue to work. v1 installation selects only the exact `stable` channel entry and the artifact matching the Runtime Host's `GOOS` and `GOARCH`; initial supported targets are `linux/amd64` and `linux/arm64`. It does not infer compatibility from version ordering or search for a close platform match.

Catalog requests and artifact downloads are bounded, context-cancelable HTTPS requests. URLs with credentials or unsupported schemes are rejected, redirects are bounded and HTTPS-only, catalog and artifact sizes are limited, and the production HTTP transport refuses loopback, private, link-local, and other non-public destinations. Artifact URLs are taken only from the validated registry document; callers cannot submit an arbitrary download URL.

## Registry document

The canonical schema is [`schemas/plugin-registry-v1.schema.json`](schemas/plugin-registry-v1.schema.json). Runtime validation is stricter than generic JSON decoding: unknown properties, invalid UTF-8, trailing JSON, duplicate plugin IDs, release versions, or platform artifacts, unsupported protocol versions, unsafe file names, invalid HTTPS URLs, malformed digests, and out-of-range sizes are rejected. The catalog is limited to 2 MiB, 256 plugins, 128 releases per plugin, and 16 artifacts per release. Channel names are reserved for `stable`, `beta`, and `development`; v1 installation currently honors `stable` only.

An empty, valid catalog is included at [`../protocol/plugin-registry-v1/empty-catalog.json`](../protocol/plugin-registry-v1/empty-catalog.json). A populated registry entry has this shape:

```json
{
  "schema_version": 1,
  "plugins": [
    {
      "id": "adapter-id",
      "name": "Adapter display name",
      "repository": "https://github.com/publisher/adapter-repository",
      "channels": {"stable": "1.2.3"},
      "releases": [
        {
          "version": "1.2.3",
          "protocol_version": 1,
          "source_commit": "0123456789abcdef0123456789abcdef01234567",
          "artifacts": [
            {
              "os": "linux",
              "arch": "amd64",
              "url": "https://github.com/publisher/adapter-repository/releases/download/v1.2.3/integrated-recorder-adapter-adapter-id",
              "filename": "integrated-recorder-adapter-adapter-id",
              "size": 123456,
              "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
            }
          ]
        }
      ]
    }
  ]
}
```

The values above illustrate field shape only and do not identify a published plugin. A production registry must use the exact artifact bytes and digest produced by the publisher's release workflow. Registry maintainers should publish a new version for changed bytes and must not silently repoint the same version to another executable.

## Install lifecycle

`GET /api/runtime/plugins` returns a bounded registry/install projection. `POST /api/runtime/plugins/refresh` fetches and validates the configured catalog. `POST /api/runtime/plugins/{id}/install` and `/update` use the selected stable release, verify it, probe its descriptor, commit the durable desired selection, and invoke the normal adapter-set reconciliation and application-generation readiness/activation path. `DELETE /api/runtime/plugins/{id}` removes it from desired sources and reconciles a new adapter set. Mutations use the Runtime Host's normal authentication and CSRF rules.

The remote desired selection is stored separately from the remote catalog, immutable artifact catalog, and active adapter set. Host-owned private staging and content-addressed artifact/source snapshots are stored below `/data/runtime/plugin-registry`. Partially downloaded files are never source candidates. Registry outage does not disable an already-installed plugin. Removing a plugin does not delete immutable adapter artifacts held by active, draining, rollback, or recording-pinned generations.

An adapter install or update changes the immutable adapter set and therefore activates a new application generation. New recordings use that generation. Existing recordings stay pinned to their original Engine and immutable adapter artifact; registry updates never authorize cross-adapter-set live handover. Application update and plugin mutations share the Host operation gate, so an application release and adapter set cannot be mixed across a generation.

## Out of scope

Registry v1 does not clone source, build binaries, accept community uploads, resolve plugin dependencies, rank plugins, implement publisher signatures/PKI, schedule automatic updates, sandbox adapters, or perform cross-adapter-set handover. The configured curated registry repository is the v1 approval authority. A future remote catalog can use this same verified-source and immutable-generation path.
