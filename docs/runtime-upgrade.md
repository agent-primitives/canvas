# Sessions runtime compatibility

Canvas builds against canonical `github.com/agent-primitives/sessions` upstream
main at `f18a2cb1454d1eea0c9e9063646a5251653bd95e`, including native approval,
authentication interceptors and guarded Resume:

- Core: `v0.2.1-0.20261019224537-f18a2cb1454d`
- Substrate integration: `v0.0.0-20261019224537-f18a2cb1454d`
- Resolved Substrate: `v0.4.1-0.20261013053953-96dffb287919`, upgraded from
  `fc0e3586a6de` through the integration's module requirement.
- Go floor: 1.27.0 (required by the integration); selected toolchain: 1.27.2.

These are ordinary Go module requirements, without fork or workspace replacements. Use
`GOWORK=off make build` and `GOWORK=off make test` to verify the shipped modules.
The Docker build-stage digest is the registry-verified multi-platform index for
`golang:1.27.2-alpine`; the app runtime image remains separately pinned.

## Journal upgrade and backup

Stop the host and Canvas before taking a consistent backup of their entire state
directories, including the journal and immutable contract archive. Preserve that
backup separately before starting the new binary. Do not copy only a live SQLite
main file: uncheckpointed changes may be in its WAL.

Opening a journal written by the prior Canvas pin, `9a3d755c1072`, migrates its
SQLite schema from version 1 to version 5, including the harness registry,
approval-reporting and authentication schemas. Migration alone does not authorize or
continue an approval; the separate local action requires explicit inspection,
decision and guarded continuation.
Migration happens on open, not only on the first new turn. An old binary refuses
the newer schema. In-place downgrade is not supported or qualified; restoring the
stopped-state backup is a separate operator action and discards later writes.

The upgrade regression uses `internal/sessionhost/testdata/9a3d755-completed.db`,
a synthetic fixture generated through the old Canvas host at `c996f60` and its
normal `9a3d755c1072` module dependencies, with no workspace replacements. Its
private temporary host made two scripted model calls, one completed parent turn
and one completed fork-child turn. It contains no operator journal or real model
output. The fixture bundle has instructions `old pin fixture instructions`, model
`fixture-model`, and output `{"strategy":"old-pin-fictional"}`. Its parent prefix
is four records; the child retains that prefix, its fork marker, and its own turn.
The regression copies the database before opening it, checks schema 1 → 5 and
byte-identical existing event rows/hashes/fences, preserves lineage, and verifies
completed replay and explicit Resume without a provider call. This is bounded
upgrade evidence, not a general journal-repair or rollback qualification.

## Substrate lifecycle and transport

The default backend still releases and deletes an actor on suspension. Current
placement calls only `Snapshot`, so the Canvas default wrapper invokes native
`Stop` exactly once before returning a journal-identity checkpoint. A failed
release/delete returns an error and does not publish a successful checkpoint;
partial-delete recovery attaches to the same suspended actor by session UID.

The retained backend instead calls native `Snapshot` once, releases the worker,
and retains the actor/checkpoint without calling `Stop`. Explicit teardown and
failed-fork cleanup remain destructive. Both policies expose stateless journal
identity references to Sessions, not native memory-snapshot references. Neither
promises snapshot-free activation: Substrate selects the actor's own checkpoint,
a template snapshot, or a fresh start. There is no per-call Boot flag.

ActorTemplates are now atespace-scoped. The existing `-template-namespace` setting
is passed as the template's **atespace**, not a Kubernetes namespace. Harness
incarnations address the atenet router with `ate-target-actor` metadata, not a
worker/pod IP. The adapter constructor and placement dialer follow those current
upstream interfaces.

## Explicit Resume semantics

An explicit runtime Resume of a modern unanswered model intent reissues the
recorded model request; it may be charged again. Another explicit Resume after a
provider failure can reissue it again. Restart and read-only verification do not
invoke the provider; a recorded completion is replayed without another call.
Provider error details remain sanitized. Producer-facing confirmation and tool
refusal are separate Canvas recovery responsibilities, not guarantees conferred
by the runtime API itself.

## Qualification boundary

Cluster deployment is not a supported setup path. Use the local workflow in
the [README](../README.md#quick-start).

Local fake-Control, scripted-provider and SQLite tests do not qualify cloud
images, a live actor router, snapshot selection, or live cluster deployment.
Approval adoption is qualified only for the separate zero-model embedded-local
simulation in [generic-canvas.md](generic-canvas.md#approve-an-exact-announcement-snapshot).
Profiles 1–4 remain unchanged. Native approval/tool-bearing sessions are never
eligible for the existing model-resend Resume control. Both SDK module paths and
Go imports are canonical; protobuf wire package identities stay `agentsessions.v1`.
The historical SQLite fixture bytes and prior model request hashes remain test
anchors, not regenerated or relabeled evidence.
